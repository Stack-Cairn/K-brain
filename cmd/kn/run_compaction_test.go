package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/acp"
	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/mcp"
	"github.com/Stack-Cairn/K-brain/internal/session"
	acpsdk "github.com/coder/acp-go-sdk"
)

func configureRunCompaction(t *testing.T, endpoint string) {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	p := cfg.Providers["testprov"]
	p.BaseURL = endpoint + "/v1"
	cfg.Providers["testprov"] = p
	m := cfg.Models["test"]
	m.Context = 400
	cfg.Models["test"] = m
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
}

func runCompactionHistory(t *testing.T, storedSystem bool) (*session.Store, string, []ai.Message) {
	t.Helper()
	home, _ := configDir()
	st, err := sessionOpen(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	id, err := st.Create(cwd(), "test", "testprov")
	if err != nil {
		t.Fatal(err)
	}
	var msgs []ai.Message
	if storedSystem {
		msgs = append(msgs, ai.Message{Role: "system", Content: "original system"})
	}
	for i := range 8 {
		msgs = append(msgs, ai.Message{Role: "user", Content: fmt.Sprintf("question %d %s", i, strings.Repeat("long history ", 200))}, ai.Message{Role: "assistant", Content: fmt.Sprintf("answer %d", i)})
	}
	if err := st.Save(id, 0, msgs, "test", "testprov"); err != nil {
		t.Fatal(err)
	}
	return st, id, msgs
}

func TestRunCompactionAcrossACPAndCLI(t *testing.T) {
	for _, storedSystem := range []bool{true, false} {
		t.Run(fmt.Sprintf("stored-system=%v", storedSystem), func(t *testing.T) {
			t.Chdir(t.TempDir())
			runFixture(t, "unused", nil)
			requests := make(chan ai.Request, 8)
			var summaries atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req ai.Request
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					return
				}
				if !req.Stream {
					n := summaries.Add(1)
					fmt.Fprintf(w, `{"choices":[{"message":{"content":"summary %d"}}],"usage":{"prompt_tokens":100,"completion_tokens":10}}`, n)
					return
				}
				requests <- req
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"continued\"}}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2,\"prompt_cache_hit_tokens\":6,\"prompt_cache_write_tokens\":2}}\n\ndata: [DONE]\n\n")
			}))
			defer srv.Close()
			configureRunCompaction(t, srv.URL)
			st, id, original := runCompactionHistory(t, storedSystem)
			prior := ai.Usage{PromptTokens: 50, CompletionTokens: 5, PromptCacheHitTokens: 30, PromptCacheWriteTokens: 5}
			if err := st.SetUsage(id, ai.UsageSummary{Total: prior, Models: map[string]ai.Usage{"prior @ original": prior}, Subagents: map[string]ai.Usage{"child @ original": {PromptTokens: 7}}}); err != nil {
				t.Fatal(err)
			}
			if _, err := runCapture(t, "", "--quiet", "--system", "cli system", "--resume", id, "CLI first"); err != nil {
				t.Fatal(err)
			}
			cliRequest := <-requests
			checkRunSummary(t, cliRequest, id, "summary 1", "cli system")
			if summaries.Load() != 1 {
				t.Fatalf("summaries = %d", summaries.Load())
			}
			bridge := acp.NewBridge("test", func(context.Context, string, map[string]mcp.ServerConfig) (*agent.Agent, *mcp.Manager, error) {
				ag := agent.New(ai.New(srv.URL+"/v1", "k"), "test", 100, "acp system")
				ag.ModelName, ag.Provider = "test", "testprov"
				return ag, nil, nil
			}, st, false, nil)
			defer bridge.CloseAll()
			sid := acpsdk.SessionId(id)
			if _, err := bridge.ResumeSession(context.Background(), acpsdk.ResumeSessionRequest{SessionId: sid, Cwd: cwd()}); err != nil {
				t.Fatal(err)
			}
			if _, err := bridge.Prompt(context.Background(), acpsdk.PromptRequest{SessionId: sid, Prompt: []acpsdk.ContentBlock{acpsdk.TextBlock("ACP next")}}); err != nil {
				t.Fatal(err)
			}
			acpRequest := <-requests
			checkRunSummary(t, acpRequest, id, "summary 1", "acp system")
			if len(acpRequest.Messages) != len(cliRequest.Messages)+2 {
				t.Fatalf("ACP history length %d, CLI %d", len(acpRequest.Messages), len(cliRequest.Messages))
			}
			for i, m := range cliRequest.Messages[1:] {
				got := acpRequest.Messages[i+1]
				if got.Role != m.Role || got.Content != m.Content {
					t.Fatalf("ACP history differs at %d", i+1)
				}
			}
			if _, err := bridge.CloseSession(context.Background(), acpsdk.CloseSessionRequest{SessionId: sid}); err != nil {
				t.Fatal(err)
			}
			if _, err := runCapture(t, "", "--quiet", "--system", "cli system", "--resume", id, "CLI again"); err != nil {
				t.Fatal(err)
			}
			checkRunSummary(t, <-requests, id, "summary 2", "cli system")
			if summaries.Load() != 2 || len(st.Compactions(id)) != 2 {
				t.Fatalf("summary counts: model=%d stored=%d", summaries.Load(), len(st.Compactions(id)))
			}
			raw := st.RawMessages(id)
			if len(raw) != len(original)+6 || !reflect.DeepEqual(raw[:len(original)], original) {
				t.Fatal("cross-client continuation overwrote raw history")
			}
			var users []string
			for _, m := range raw[len(original):] {
				if m.Role == "user" {
					users = append(users, m.Content)
				}
			}
			if strings.Join(users, "|") != "CLI first|ACP next|CLI again" {
				t.Fatalf("new turns lost or duplicated: %v", users)
			}
			meta, _, err := st.Load(id)
			if err != nil {
				t.Fatal(err)
			}
			if meta.UsageIn != 280 || meta.UsageOut != 31 || meta.UsageCached != 48 || meta.UsageCacheWrite != 11 || meta.ModelUsage["prior @ original"].PromptTokens != 50 || meta.ModelUsage["test @ testprov"].PromptTokens != 230 || meta.SubUsage["child @ original"].PromptTokens != 7 {
				t.Fatalf("cross-client usage lost or attributed to wrong model: %+v", meta)
			}
		})
	}
}

func checkRunSummary(t *testing.T, req ai.Request, id, summary, system string) {
	t.Helper()
	if req.PromptCacheKey != id {
		t.Fatalf("cache key %q, want %q", req.PromptCacheKey, id)
	}
	if len(req.Messages) < 3 || req.Messages[0].Content != system || req.Messages[1].Content != "Summary of the conversation so far:\n\n"+summary {
		t.Fatalf("summary or current system missing: %+v", req.Messages)
	}
	for _, m := range req.Messages[2:] {
		if m.Role == "system" && strings.HasPrefix(m.Content, "Summary of the conversation so far:") {
			t.Fatal("obsolete summary retained")
		}
	}
}

func TestRunTimeoutAfterCompactionPersistsSummary(t *testing.T) {
	t.Chdir(t.TempDir())
	runFixture(t, "unused", nil)
	var summaries atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ai.Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if !req.Stream {
			summaries.Add(1)
			fmt.Fprint(w, `{"choices":[{"message":{"content":"saved before timeout"}}],"usage":{"prompt_tokens":100,"completion_tokens":10,"prompt_cache_hit_tokens":60,"prompt_cache_write_tokens":20}}`)
			return
		}
		<-r.Context().Done()
	}))
	defer srv.Close()
	configureRunCompaction(t, srv.URL)
	st, id, original := runCompactionHistory(t, true)
	_, err := runCapture(t, "", "--quiet", "--system", "sys", "--timeout", "1s", "--resume", id, "interrupted followup")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout = %v", err)
	}
	if summaries.Load() != 1 || len(st.Compactions(id)) != 1 {
		t.Fatal("completed compaction lost on timeout")
	}
	meta, _, loadErr := st.Load(id)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if meta.UsageIn != 100 || meta.UsageOut != 10 || meta.UsageCached != 60 || meta.UsageCacheWrite != 20 || meta.ModelUsage["test @ testprov"].PromptTokens != 100 {
		t.Fatalf("completed compaction usage lost on timeout: %+v", meta)
	}
	raw := st.RawMessages(id)
	if len(raw) != len(original)+1 || !reflect.DeepEqual(raw[:len(original)], original) {
		t.Fatal("timeout overwrote old history")
	}
	_, msgs, err := st.History(id, []ai.Message{{Role: "system", Content: "new system"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) < 3 || msgs[1].Content != "Summary of the conversation so far:\n\nsaved before timeout" || msgs[len(msgs)-1].Content != "interrupted followup" {
		t.Fatalf("timeout restore = %+v", msgs)
	}
}
