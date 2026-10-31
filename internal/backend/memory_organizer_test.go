package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/memory"
	"github.com/Stack-Cairn/K-brain/internal/memoryruntime"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

func TestMemoryOrganizerProductionSchedulerStartupAndShutdown(t *testing.T) {
	root := t.TempDir()
	sessions, err := session.Open(filepath.Join(root, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	defer sessions.Close()
	memoryStore, err := memory.OpenStore(filepath.Join(root, "memory"))
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(Options{
		Store: sessions, EventDir: filepath.Join(root, "events"), MemoryRoot: memoryStore.Root(), DefaultCWD: root,
		Factory: func(context.Context, string, protocol.ModelRef) (*agent.Agent, error) {
			return agent.New(&scriptedClient{}, "model", 128, ""), nil
		},
		MemoryRuntimeFactory: func(context.Context, string, protocol.ModelRef) (*memoryruntime.Runtime, error) {
			return memoryruntime.New(memoryruntime.Config{
				Store: memoryStore, ResolveModel: func(context.Context, string) (ai.Client, string, error) {
					return nil, "", nil
				},
			})
		},
		MemoryOrganizerInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if server.organizerRuntime == nil || server.organizerCancel == nil {
		t.Fatal("memory organizer scheduler was not started")
	}
	t.Cleanup(func() {
		if closeErr := server.Close(); closeErr != nil {
			t.Errorf("close backend: %v", closeErr)
		}
	})
	deadline := time.Now().Add(2 * time.Second)
	for {
		runs, listErr := memoryStore.ListOrganizeRuns()
		if listErr != nil {
			t.Fatal(listErr)
		}
		if len(runs["runs"].([]memory.OrganizeRun)) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("production organizer scheduler did not run")
		}
		time.Sleep(time.Millisecond)
	}
	// Close waits for the scheduler goroutine, so a run that ticked before Close
	// returned is legitimate; only runs recorded after it are not.
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	atClose, err := memoryStore.ListOrganizeRuns()
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	afterClose, err := memoryStore.ListOrganizeRuns()
	if err != nil {
		t.Fatal(err)
	}
	if len(afterClose["runs"].([]memory.OrganizeRun)) != len(atClose["runs"].([]memory.OrganizeRun)) {
		t.Fatal("memory organizer scheduler continued after backend close")
	}
}

func TestMemoryOrganizerHTTPPersistence(t *testing.T) {
	root := t.TempDir()
	store, err := memory.OpenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, slug := range []string{"one", "two"} {
		if _, err := store.Write(memory.WriteArgs{Slug: slug, Scope: "global", MemoryType: "reference", Description: "same", Body: "same"}); err != nil {
			t.Fatal(err)
		}
	}
	var factories atomic.Int32
	newServer := func() *httptest.Server {
		reopened, err := memory.OpenStore(root)
		if err != nil {
			t.Fatal(err)
		}
		return httptest.NewServer(&Server{token: "organizer-token", memoryStore: reopened, memoryRuntimeFactory: func(context.Context, string, protocol.ModelRef) (*memoryruntime.Runtime, error) {
			factories.Add(1)
			fresh, err := memory.OpenStore(root)
			if err != nil {
				return nil, err
			}
			return memoryruntime.New(memoryruntime.Config{Store: fresh, OrganizerModel: "organizer-model", Now: func() time.Time { return time.UnixMilli(1700000000123) }, ResolveModel: func(context.Context, string) (ai.Client, string, error) {
				return nil, "", fmt.Errorf("model execution is outside this test")
			}})
		}})
	}
	server := newServer()
	defer server.Close()
	post := func(url, body string) (map[string]any, int, error) {
		req, err := http.NewRequest(http.MethodPost, url+"/v1/memory/organize", strings.NewReader(body))
		if err != nil {
			return nil, 0, err
		}
		req.Header.Set("Authorization", "Bearer organizer-token")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, 0, err
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, resp.StatusCode, err
		}
		var out map[string]any
		err = json.Unmarshal(data, &out)
		return out, resp.StatusCode, err
	}
	request := func(url, body string) map[string]any {
		t.Helper()
		out, status, err := post(url, body)
		if err != nil || status != http.StatusOK {
			t.Fatalf("request %s: status=%d response=%v err=%v", body, status, out, err)
		}
		return out
	}
	run := request(server.URL, `{"action":"run"}`)
	if run["status"] != "pending_review" || len(run["pending"].([]any)) != 1 || run["scanned"] != float64(2) {
		t.Fatalf("run=%v", run)
	}
	id := run["runId"].(string)
	history := request(server.URL, `{"action":"history"}`)["runs"].([]any)
	if len(history) != 1 {
		t.Fatalf("history=%v", history)
	}
	record := history[0].(map[string]any)
	for _, field := range []string{"runId", "model", "pending", "status"} {
		if !reflect.DeepEqual(record[field], run[field]) {
			t.Fatalf("history lost %s: run=%v stored=%v", field, run[field], record[field])
		}
	}
	if record["createdAt"] != float64(1700000000123) || record["updatedAt"] != float64(1700000000123) {
		t.Fatalf("history timestamps lost: %v", record)
	}
	// Reopen the HTTP server and store before applying the persisted proposal.
	server.Close()
	server = newServer()
	defer server.Close()
	body := fmt.Sprintf(`{"action":"apply","runId":%q}`, id)
	applied := request(server.URL, body)
	if applied["status"] != "completed" || applied["applied"] != float64(1) || applied["pending"] != nil {
		t.Fatalf("applied=%v", applied)
	}
	if applied["model"] != run["model"] || applied["createdAt"] != run["createdAt"] {
		t.Fatalf("metadata changed: %v", applied)
	}
	if got := request(server.URL, body); !reflect.DeepEqual(got, applied) {
		t.Fatalf("repeat changed result: %v != %v", got, applied)
	}
	history = request(server.URL, `{"action":"history"}`)["runs"].([]any)
	if len(history) != 1 || history[0].(map[string]any)["status"] != "completed" {
		t.Fatalf("duplicate or stale history=%v", history)
	}
	entries, err := store.List(memory.ListArgs{Limit: 100})
	if err != nil || len(entries.Entries) != 1 {
		t.Fatalf("entries=%v err=%v", entries, err)
	}
	rejections, err := store.RecentRejections()
	if err != nil || len(rejections["entries"].([]memory.Rejection)) != 1 {
		t.Fatalf("rejections=%v err=%v", rejections, err)
	}
	if factories.Load() != 3 {
		t.Fatalf("requests reused runtime: %d", factories.Load())
	}

	// Independent runtimes racing the same append must not append twice.
	desc, appendBody := "append target", "once"
	_, err = store.CreateOrganizeRun(memory.OrganizeRun{"runId": "concurrent", "status": "pending_review", "createdAt": int64(1700000000123), "updatedAt": int64(1700000000123), "model": "saved-model", "warnings": []string{"retained warning"}, "pending": []memory.Decision{{Op: "update", Scope: "global", Slug: entries.Entries[0].Slug, Mode: "append", Description: &desc, Body: &appendBody}}})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			out, status, err := post(server.URL, `{"action":"apply","runId":"concurrent"}`)
			if err != nil || (status != http.StatusOK && !(status == http.StatusBadRequest && strings.Contains(fmt.Sprint(out["error"]), "already being applied"))) {
				t.Errorf("concurrent status=%d out=%v err=%v", status, out, err)
			}
		}()
	}
	close(start)
	wg.Wait()
	final := request(server.URL, `{"action":"apply","runId":"concurrent"}`)
	if final["applied"] != float64(1) || final["model"] != "saved-model" || !reflect.DeepEqual(final["warnings"], []any{"retained warning"}) {
		t.Fatalf("final=%v", final)
	}
	target, err := store.Read(memory.ReadArgs{Scope: "global", Slug: entries.Entries[0].Slug})
	if err != nil || strings.Count(target.Body, "once") != 1 {
		t.Fatalf("replayed append: %q err=%v", target.Body, err)
	}
	history = request(server.URL, `{"action":"history"}`)["runs"].([]any)
	if len(history) != 2 {
		t.Fatalf("duplicate records=%v", history)
	}
}
