package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/workflow"
)

func workflowServer(t *testing.T, subCalls *atomic.Int64) *httptest.Server {
	t.Helper()
	script := `export const meta = { name: 'demo', description: 'd' }
const r = await parallel([0, 1].map(i => () => agent('task ' + i, { label: 'w' + i })))
return 'workflow done: ' + r.filter(Boolean).length + ' agents'`

	callArgs, _ := json.Marshal(map[string]any{"script": script})
	delta := map[string]any{"choices": []any{map[string]any{"delta": map[string]any{
		"tool_calls": []any{map[string]any{
			"index": 0, "id": "w1", "type": "function",
			"function": map[string]any{"name": "workflow", "arguments": string(callArgs)},
		}},
	}}}}
	callLine, _ := json.Marshal(delta)
	var parentCalls atomic.Int64
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ai.Request
		json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "text/event-stream")

		isSub := len(req.Messages) > 0 && strings.Contains(req.Messages[0].Content, "You are a subagent inside k-brain")
		if isSub {
			subCalls.Add(1)
			fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"sub report"},"finish_reason":"stop"}]}`+"\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}

		n := parentCalls.Add(1)
		if n == 1 {
			fmt.Fprintf(w, "data: %s\n\n", callLine)
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}

		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"parent final"},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
}

func TestWorkflowToolEndToEnd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("K_BRAIN_HOME", home)

	var subCalls atomic.Int64
	srv := workflowServer(t, &subCalls)
	defer srv.Close()

	ag := New(ai.New(srv.URL, "k"), "m", 100000, "sys", WithExperimental([]string{FeatureWorkflows}))

	found := false
	for i := range ag.Tools {
		if ag.Tools[i].Def.Function.Name == "workflow" {
			found = true
		}
	}
	if !found {
		t.Fatal("workflow tool not registered in agent.New")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var steered []string
	final, err := ag.Turn(ctx, "run a workflow", Events{
		OnSteer: func(s string) { steered = append(steered, s) },
	})
	if err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for subCalls.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if subCalls.Load() != 2 {
		t.Fatalf("expected 2 workflow subagent calls, got %d", subCalls.Load())
	}
	_ = final

	mgr := ag.Workflows()
	deadline = time.Now().Add(5 * time.Second)
	for {
		runs := mgr.List()
		if len(runs) == 1 && runs[0].Status == "complete" {
			snap, _ := mgr.Snapshot(runs[0].ID)
			if snap.Result == nil || !strings.Contains(fmt.Sprint(snap.Result), "workflow done: 2 agents") {
				t.Fatalf("workflow result: %v", snap.Result)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("workflow did not complete: runs=%+v", runs)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func schemaServer(t *testing.T, calls *atomic.Int64, callArgs string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ai.Request
		json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "text/event-stream")
		isSub := len(req.Messages) > 0 && strings.Contains(req.Messages[0].Content, "You are a subagent inside k-brain")
		if !isSub {
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		n := calls.Add(1)
		if n == 1 {

			delta := map[string]any{"choices": []any{map[string]any{"delta": map[string]any{
				"tool_calls": []any{map[string]any{
					"index": 0, "id": "s1", "type": "function",
					"function": map[string]any{"name": "structured_output", "arguments": callArgs},
				}},
			}}}}
			line, _ := json.Marshal(delta)
			fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", line)
			return
		}

		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"no tool"},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
}

func TestRunWorkflowAgentStructuredOutput(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	var calls atomic.Int64
	wantArgs := `{"verdict":"pass","confidence":0.9}`
	srv := schemaServer(t, &calls, wantArgs)
	defer srv.Close()

	ag := New(ai.New(srv.URL, "k"), "m", 100000, "sys", WithExperimental([]string{FeatureWorkflows}))
	res, _, err := ag.runWorkflowAgent(context.Background(), workflow.AgentRequest{
		Prompt:  "produce a verdict",
		Options: workflow.AgentOptions{Schema: json.RawMessage(`{"type":"object","properties":{"verdict":{"type":"string"},"confidence":{"type":"number"}},"required":["verdict"]}`)},
	})
	if err != nil {
		t.Fatalf("runWorkflowAgent schema path errored: %v", err)
	}
	m, ok := res.(map[string]any)
	if !ok || m["verdict"] != "pass" {
		t.Fatalf("schema result = %#v, want {verdict:pass}", res)
	}

	if calls.Load() != 2 {
		t.Fatalf("expected 2 sub turns (tool call + follow-up), got %d", calls.Load())
	}
}

func TestRunWorkflowAgentStructuredOutputRepair(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	var calls atomic.Int64

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		calls.Add(1)
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"just text"},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	ag := New(ai.New(srv.URL, "k"), "m", 100000, "sys", WithExperimental([]string{FeatureWorkflows}))
	_, _, err := ag.runWorkflowAgent(context.Background(), workflow.AgentRequest{
		Prompt:  "produce a verdict",
		Options: workflow.AgentOptions{Schema: json.RawMessage(`{"type":"object"}`)},
	})
	if err == nil || !strings.Contains(err.Error(), "did not produce valid structured_output") {
		t.Fatalf("expected structured_output error, got %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("expected 2 sub turns (initial + repair), got %d", calls.Load())
	}
}

func TestRunWorkflowAgentModelOverrideError(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	ag := New(ai.New("http://unused", "k"), "m", 100, "sys", WithExperimental([]string{FeatureWorkflows}))

	_, _, err := ag.runWorkflowAgent(context.Background(), workflow.AgentRequest{
		Prompt: "x", Model: "unresolvable-model",
	})
	if err == nil || !strings.Contains(err.Error(), "model override") {
		t.Fatalf("expected model override error, got %v", err)
	}
}

func TestResolveWorkflowScript(t *testing.T) {

	s, err := resolveWorkflowScript("export const meta = {}", "")
	if err != nil || s != "export const meta = {}" {
		t.Fatalf("inline: %q %v", s, err)
	}

	s, err = resolveWorkflowScript("```js\nexport const meta = {}\n```", "")
	if err != nil || s != "export const meta = {}" {
		t.Fatalf("fenced: %q %v", s, err)
	}

	dir := t.TempDir()
	p := dir + "/wf.js"
	if err := writeFile(p, "export const meta = {name:'x'}"); err != nil {
		t.Fatal(err)
	}
	s, err = resolveWorkflowScript("", p)
	if err != nil || !strings.Contains(s, "name:'x'") {
		t.Fatalf("path: %q %v", s, err)
	}

	if _, err = resolveWorkflowScript("", ""); err == nil {
		t.Fatal("expected error with neither script nor scriptPath")
	}
}

func writeFile(p, content string) error {
	return os.WriteFile(p, []byte(content), 0o600)
}

func hasTool(a *Agent, name string) bool {
	for i := range a.Tools {
		if a.Tools[i].Def.Function.Name == name {
			return true
		}
	}
	return false
}

func TestWorkflowToolGatedByExperimental(t *testing.T) {
	c := ai.New("http://unused", "k")

	def := New(c, "m", 100, "sys")
	if hasTool(def, "workflow") {
		t.Fatal("workflow tool built without experimental opt-in")
	}
	if got := def.Experimental(); len(got) != 0 {
		t.Fatalf("default Experimental() = %v, want empty", got)
	}

	on := New(c, "m", 100, "sys", WithExperimental([]string{FeatureWorkflows}))
	if !hasTool(on, "workflow") {
		t.Fatal("workflow tool not built with experimental opt-in")
	}
	if got := on.Experimental(); len(got) != 1 || got[0] != FeatureWorkflows {
		t.Fatalf("Experimental() = %v, want [workflows]", got)
	}

	other := New(c, "m", 100, "sys", WithExperimental([]string{"future-thing"}))
	if hasTool(other, "workflow") {
		t.Fatal("workflow tool built by an unrelated experimental name")
	}
}
