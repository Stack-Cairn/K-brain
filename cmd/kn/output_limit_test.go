package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/config"
)

func TestRunJSONOutputLimitAndResume(t *testing.T) {
	runFixture(t, "unused", nil)
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requests.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		if n > 1 && (!strings.Contains(string(body), "partial") || strings.Contains(string(body), `"stop_reason"`)) {
			t.Errorf("wrong resumed input: %s", body)
		}
		if n > 1 && n <= 1+agent.MaxOutputLimitContinuations && !strings.Contains(string(body), "output token limit") {
			t.Errorf("continuation %d has no reminder: %s", n, body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		// The run continues a truncated reply up to the cap; every attempt is cut off,
		// so the run still ends at the length limit.
		if n <= 1+agent.MaxOutputLimitContinuations {
			fmt.Fprint(w, "data: "+`{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"message","content":[{"text":"partial"}]}],"usage":{"input_tokens":17,"output_tokens":3}}}`+"\n\n")
		} else {
			fmt.Fprint(w, "data: "+`{"type":"response.completed","response":{"status":"completed","output":[{"type":"message","content":[{"text":"done"}]}],"usage":{"input_tokens":17,"output_tokens":3}}}`+"\n\n")
		}
	}))
	defer srv.Close()
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	provider := cfg.Providers["testprov"]
	provider.BaseURL, provider.API = srv.URL, "openai-responses"
	cfg.Providers["testprov"] = provider
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	out, err := runCapture(t, "", "--format", "json", "--quiet", "first")
	if err != nil {
		t.Fatal(err)
	}
	events := decodeRunEvents(t, out)
	done := events[len(events)-1]
	var streamed strings.Builder
	for _, event := range events {
		if event["type"] == "text" {
			streamed.WriteString(event["delta"])
		}
	}
	if done["type"] != "done" || done["stopReason"] != "length" || !strings.HasPrefix(done["text"], "partial\n") || !strings.HasPrefix(streamed.String(), "partial\n") {
		t.Fatalf("partial JSON output: %s", out)
	}
	if got := strings.Count(streamed.String(), "[response truncated by output limit]"); got != 1+agent.MaxOutputLimitContinuations || requests.Load() != 1+agent.MaxOutputLimitContinuations {
		t.Fatalf("continuations: notes=%d requests=%d: %s", got, requests.Load(), out)
	}
	dir, err := configDir()
	if err != nil {
		t.Fatal(err)
	}
	st, err := sessionOpen(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	metas, err := st.Recent(10)
	if err != nil || len(metas) != 1 {
		t.Fatalf("sessions=%+v error=%v", metas, err)
	}
	id := metas[0].ID
	_, msgs, err := st.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	if msgs[len(msgs)-1].StopReason != ai.StopReasonLength {
		t.Fatal("partial reply stop reason not saved")
	}
	out, err = runCapture(t, "", "--format", "json", "--quiet", "--resume", id, "continue")
	if err != nil {
		t.Fatal(err)
	}
	events = decodeRunEvents(t, out)
	done = events[len(events)-1]
	if done["stopReason"] != "stop" || done["text"] != "done" || requests.Load() != 2+agent.MaxOutputLimitContinuations {
		t.Fatalf("resumed output: %s; requests=%d", out, requests.Load())
	}
	meta, _, err := st.Load(id)
	if calls := 2 + agent.MaxOutputLimitContinuations; err != nil || meta.UsageIn != 17*calls || meta.UsageOut != 3*calls {
		t.Fatalf("restored usage=%+v error=%v", meta, err)
	}
}
