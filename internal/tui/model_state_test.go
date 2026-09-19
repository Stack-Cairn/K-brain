package tui

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/session/recording"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

func TestModelSwitchRetainsLiveTasksAndSessionState(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	t.Chdir(t.TempDir())
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	type request struct {
		ai.Request
		CacheKey string `json:"prompt_cache_key"`
	}
	requests := make(chan request, 12)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		requests <- req
		if req.Messages[len(req.Messages)-1].Content == "background blocked" {
			close(entered)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"reply\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2}}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()
	m := tasksModelStore(t, srv.URL)
	m.cfg.Models["worker"] = config.Model{Providers: []string{"p"}, Context: 64000, MaxOut: 8000}
	m.agent.ModelName, m.agent.Provider = "m", "p"
	m.prepareHistory()
	m.wireTasks()
	original, waits := m.agent, m.agent.Waits()
	m.agent.AddUsage(ai.Usage{PromptTokens: 100, CompletionTokens: 10})
	if result := tools.Execute(t.Context(), m.agent.Tools, "todowrite", []byte(`{"todos":[{"id":"t1","content":"retain plan","status":"pending"}]}`)); strings.HasPrefix(result, "Error:") {
		t.Fatal(result)
	}
	m.agent.SteerImages("retain queued guidance", nil)
	notices := make(chan string, 8)
	m.agent.OnOrphanedSteer = func(text string) { notices <- text }
	task := m.agent.StartBackground("running task", "background blocked", agent.SubModel{})
	defer m.agent.Tasks().Cancel(task.ID)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("task did not start")
	}
	first := <-requests
	stream := make(chan string, 8)
	_, _, live, unsubscribe := m.agent.Tasks().WatchTask(task.ID, agent.Events{OnText: func(text string) { stream <- text }})
	defer unsubscribe()
	if !live {
		t.Fatal("task is not live")
	}
	m.previewModel(modelItem{model: "worker", provider: "p"})
	if m.agent != original || m.agent.Waits() != waits {
		t.Fatal("preview replaced session runtime")
	}
	if m.cfg.DefaultModel != "m" {
		t.Fatal("preview changed saved default")
	}
	m.switchModel("worker", "p", false)
	if m.modelName != "worker" || m.agent.ContextLimit != 64000 || m.agent.MaxTokens != 8000 {
		t.Fatal("model configuration not selected")
	}
	if m.agent.Usage().PromptTokens != 100 || !strings.Contains(m.agent.TodosJSON(), "retain plan") || m.runningTasks() != 1 {
		t.Fatal("session state lost")
	}
	if first.Model != "m" || first.CacheKey != m.sessionID+"/"+task.ID {
		t.Fatalf("child route = %+v", first)
	}
	unblock()
	waitSettled(t, task)
	select {
	case text := <-notices:
		if !strings.Contains(text, task.ID) {
			t.Fatalf("notice = %q", text)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("lost completion notice")
	}
	select {
	case text := <-stream:
		if text != "reply" {
			t.Fatalf("stream = %q", text)
		}
	default:
		t.Fatal("lost task subscription")
	}
	if _, err := m.agent.FollowupTask(t.Context(), task.ID, "followup", agent.Events{}); err != nil {
		t.Fatal(err)
	}
	if req := <-requests; req.Model != "m" || req.CacheKey != first.CacheKey {
		t.Fatalf("existing child switched model or cache: %+v", req)
	}
	next := m.agent.StartBackground("new task", "new child", agent.SubModel{})
	waitSettled(t, next)
	select {
	case <-notices:
	case <-time.After(5 * time.Second):
		t.Fatal("new task did not notify")
	}
	if req := <-requests; req.Model != "worker" || req.CacheKey != m.sessionID+"/"+next.ID {
		t.Fatalf("new child did not inherit model: %+v", req)
	}
	if _, err := m.agent.Turn(t.Context(), "main next", agent.Events{}); err != nil {
		t.Fatal(err)
	}
	main := <-requests
	if main.Model != "worker" || main.CacheKey != m.sessionID {
		t.Fatalf("main route = %+v", main)
	}
	main = <-requests
	if main.Model != "worker" || main.CacheKey != m.sessionID {
		t.Fatalf("steered route = %+v", main)
	}
	var queued, todo bool
	for _, msg := range main.Messages {
		queued = queued || strings.Contains(msg.Content, "retain queued guidance")
		todo = todo || strings.Contains(msg.Content, "retain plan")
	}
	if !queued || !todo {
		t.Fatalf("queued=%v todo=%v", queued, todo)
	}
	if got := m.agent.SubUsage(); got["m @ p"].PromptTokens != 20 || got["worker @ p"].PromptTokens != 10 {
		t.Fatalf("child usage = %+v", got)
	}
	if !m.persist() {
		t.Fatal("persistence failed")
	}
	meta, _, err := m.store.Load(m.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Model != "worker" || meta.ModelUsage["m @ p"].PromptTokens != 100 || meta.ModelUsage["worker @ p"].PromptTokens != 20 {
		t.Fatalf("saved usage = %+v", meta)
	}
	backend := agent.New(ai.New(srv.URL, "key"), "m", 100, "backend system")
	backend.ModelName, backend.Provider = "m", "p"
	backend.SetSessionID(m.sessionID)
	recorder, err := recording.Open(m.store, m.sessionID, backend)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Turn(t.Context(), "backend next", recorder.Events()); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Save(); err != nil {
		t.Fatal(err)
	}
	if req := <-requests; req.Model != "m" || req.CacheKey != m.sessionID {
		t.Fatalf("backend route = %+v", req)
	}
	if err := m.resume(m.sessionID); err != nil {
		t.Fatal(err)
	}
	if m.agent.Usage().PromptTokens != 130 || m.agent.ModelUsage()["m @ p"].PromptTokens != 110 || m.agent.ModelUsage()["worker @ p"].PromptTokens != 20 || len(m.agent.Tasks().List()) != 2 {
		t.Fatal("resume lost usage or tasks")
	}
}

func TestModelSwitchWhileBusyOrInvalidLeavesStateUntouched(t *testing.T) {
	m := taskmodelCfgModel("https://unused.invalid/v1")
	original := m.agent
	m.busy = true
	m.switchModel("worker", "p", true)
	m.previewModel(modelItem{model: "worker", provider: "p"})
	if m.agent != original || m.modelName != "m" || m.cfg.DefaultModel != "m" {
		t.Fatal("busy switch modified session")
	}
	m.busy = false
	m.switchModel("worker", "missing-provider", true)
	if m.agent != original || m.modelName != "m" || m.cfg.DefaultModel != "m" {
		t.Fatal("failed switch modified session")
	}
}

func TestModelSwitchPreservesTaskCancellation(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	entered, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ai.Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		close(entered)
		select {
		case <-r.Context().Done():
			close(cancelled)
		case <-release:
		}
	}))
	defer srv.Close()
	defer close(release)
	m := taskmodelCfgModel(srv.URL)
	task := m.agent.StartBackground("cancel after switch", "wait", agent.SubModel{})
	defer m.agent.Tasks().Cancel(task.ID)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("task did not start")
	}
	m.switchModel("worker", "p", false)
	if m.modelName != "worker" || !m.agent.Tasks().Cancel(task.ID) {
		t.Fatal("switch lost task cancellation")
	}
	waitSettled(t, task)
	if got, ok := m.agent.Tasks().Get(task.ID); !ok || got.Status != agent.TaskCancelled {
		t.Fatalf("task after cancellation = %+v", got)
	}
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("task HTTP request was not cancelled")
	}
}

func TestSessionCostRetainsModelAttribution(t *testing.T) {
	m := taskmodelCfgModel("https://unused.invalid/v1")
	m.agent.Provider = "p"
	m.catalogs = map[string]config.Catalog{"p": {Models: []config.ModelInfoLite{{ID: "m", Pricing: &ai.TokenRates{Input: 1e-6, Output: 2e-6}}, {ID: "worker", Pricing: &ai.TokenRates{Input: 3e-6, Output: 4e-6}}}}}
	m.agent.AddUsage(ai.Usage{PromptTokens: 1000000, CompletionTokens: 1000000})
	m.switchModel("worker", "p", false)
	m.agent.AddUsage(ai.Usage{PromptTokens: 1000000, CompletionTokens: 1000000})
	got, ok := m.sessionCost()
	if !ok || math.Abs(got-10) > 1e-9 {
		t.Fatalf("cost = %v, %v; want 10", got, ok)
	}
}

func TestUsageCostKeepsProviderAttribution(t *testing.T) {
	m := taskmodelCfgModel("https://unused.invalid/v1")
	m.catalogs = map[string]config.Catalog{
		"p": {Models: []config.ModelInfoLite{{ID: "shared", Pricing: &ai.TokenRates{Input: 1e-6, Output: 2e-6}}}},
		"q": {Models: []config.ModelInfoLite{{ID: "shared", Pricing: &ai.TokenRates{Input: 3e-6, Output: 4e-6}}}},
	}
	u := ai.Usage{PromptTokens: 1000000, CompletionTokens: 1000000}
	if got, ok := m.compactCost(agent.CompactInfo{Model: "shared @ api.example", Provider: "q", Usage: u}); !ok || got != 7 {
		t.Fatalf("compaction cost = %v, %v", got, ok)
	}
	for _, provider := range []string{"missing", ""} {
		if _, ok := m.usageCost("shared", provider, u); ok {
			t.Fatalf("guessed pricing for provider %q", provider)
		}
	}
}
