package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

type runWriterFunc func([]byte) (int, error)

func (f runWriterFunc) Write(p []byte) (int, error) { return f(p) }

func decodeRunEvents(t *testing.T, data string) []map[string]string {
	t.Helper()
	var events []map[string]string
	decoder := json.NewDecoder(strings.NewReader(data))
	for {
		var event map[string]string
		err := decoder.Decode(&event)
		if err == io.EOF {
			return events
		}
		if err != nil {
			t.Fatalf("invalid event stream: %v\n%s", err, data)
		}
		events = append(events, event)
	}
}

func TestRunOutputConcurrentEvents(t *testing.T) {
	var buf bytes.Buffer
	o := &runOutput{writer: &buf, json: true}
	ev := o.events(nil)
	var wg sync.WaitGroup
	for i := range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := fmt.Sprint(i)
			ev.OnToolStart(id, "bash", `{"command":"echo test"}`)
			ev.OnToolOutput(id, "first\n")
			ev.OnToolOutput(id, "first\nsecond\n")
			ev.OnToolEnd(id, "bash", "first\nsecond\n")
		}()
	}
	wg.Wait()
	if err := o.finish("done", "", nil); err != nil {
		t.Fatal(err)
	}
	before := buf.String()
	ev.OnText("late text")
	ev.OnToolEnd("late", "bash", "late output")
	if err := o.finish("duplicate", "", nil); err != nil || before != buf.String() {
		t.Fatal("output continued after terminal event")
	}
	events := decodeRunEvents(t, buf.String())
	if len(events) != 401 || events[400]["type"] != "done" {
		t.Fatalf("events=%d, expected 400 tool events then done", len(events))
	}
	byID := map[string][]map[string]string{}
	for _, event := range events[:400] {
		byID[event["id"]] = append(byID[event["id"]], event)
	}
	if len(byID) != 100 {
		t.Fatalf("tool IDs lost: %d", len(byID))
	}
	for id, calls := range byID {
		if len(calls) != 4 || calls[0]["type"] != "tool_start" || calls[3]["type"] != "tool_end" || calls[1]["output"] != "first\n" || calls[2]["output"] != "first\nsecond\n" {
			t.Fatalf("incorrect event sequence for %s: %+v", id, calls)
		}
	}
}

func TestRunOutputFailureCancelsAndStaysFailed(t *testing.T) {
	broken := errors.New("broken output")
	for _, jsonMode := range []bool{false, true} {
		for _, failure := range []error{broken, io.ErrShortWrite} {
			t.Run(fmt.Sprintf("json=%v/%v", jsonMode, failure), func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				writes := 0
				o := &runOutput{json: jsonMode, cancel: cancel, writer: runWriterFunc(func(p []byte) (int, error) {
					writes++
					if failure == io.ErrShortWrite {
						return len(p) - 1, nil
					}
					return 0, failure
				})}
				ev := o.events(nil)
				ev.OnText("hello")
				if ctx.Err() != context.Canceled {
					t.Fatal("output failure did not cancel execution")
				}
				ev.OnText("ignored")
				if err := o.finish("false success", "", nil); !errors.Is(err, failure) {
					t.Fatalf("write error lost: %v", err)
				}
				if writes != 1 {
					t.Fatalf("retried a broken stream %d times", writes)
				}
			})
		}
	}
}

func TestRunOutputTerminalFailure(t *testing.T) {
	for _, jsonMode := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		o := &runOutput{json: jsonMode, cancel: cancel, writer: runWriterFunc(func([]byte) (int, error) {
			return 0, io.ErrClosedPipe
		})}
		err := o.finish("done", "", nil)
		if !errors.Is(err, io.ErrClosedPipe) || ctx.Err() != context.Canceled {
			t.Fatalf("terminal output failure hidden: %v, %v", err, ctx.Err())
		}
		cancel()
	}
}

func TestRunJSONStartupErrors(t *testing.T) {
	runFixture(t, "unused", nil)
	for _, args := range [][]string{
		{"--max-turns", "-1", "hello"},
		{"--system-file", filepath.Join(t.TempDir(), "missing"), "hello"},
		{"--resume", "missing", "hello"},
	} {
		out, err := runCapture(t, "", append([]string{"--format", "json", "--quiet"}, args...)...)
		if err == nil {
			t.Fatal("expected startup failure")
		}
		events := decodeRunEvents(t, out)
		if len(events) != 1 || events[0]["type"] != "error" || events[0]["error"] != err.Error() {
			t.Fatalf("startup error missing from stream: %s; %v", out, err)
		}
	}
}

func TestRunBrokenOutputStopsBeforeToolExecution(t *testing.T) {
	for _, format := range []string{"text", "json"} {
		t.Run(format, func(t *testing.T) {
			runFixture(t, "unused", nil)
			target := filepath.Join(t.TempDir(), "must-not-exist.txt")
			var requests atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "text/event-stream")
				if format == "text" {
					fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n")
				}
				args, _ := json.Marshal(map[string]string{"path": target, "content": "unexpected write"})
				encoded, _ := json.Marshal(string(args))
				fmt.Fprintf(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"write-1","type":"function","function":{"name":"write","arguments":%s}}]},"finish_reason":"tool_calls"}]}`+"\n\ndata: [DONE]\n\n", encoded)
			}))
			defer srv.Close()
			cfg, err := config.Load()
			if err != nil {
				t.Fatal(err)
			}
			p := cfg.Providers["testprov"]
			p.BaseURL = srv.URL + "/v1"
			cfg.Providers["testprov"] = p
			if err := cfg.Save(); err != nil {
				t.Fatal(err)
			}
			in, err := os.Open(os.DevNull)
			if err != nil {
				t.Fatal(err)
			}
			defer in.Close()
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			r.Close()
			defer w.Close()
			oldIn, oldOut := os.Stdin, os.Stdout
			os.Stdin, os.Stdout = in, w
			defer func() { os.Stdin, os.Stdout = oldIn, oldOut }()
			err = runCLI([]string{"--format", format, "--quiet", "--no-session", "--timeout", "5s", "write a file"})
			if err == nil || !strings.Contains(err.Error(), "stdout:") || errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("broken output did not fail promptly: %v", err)
			}
			if requests.Load() != 1 {
				t.Fatalf("continued model loop after output failure: %d", requests.Load())
			}
			if _, err := os.Stat(target); !os.IsNotExist(err) {
				t.Fatalf("tool executed after output failure: %v", err)
			}
		})
	}
}

func TestRunJSONStdinTimeoutEmitsNormalizedError(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	old := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = old }()
	var runErr error
	start := time.Now()
	out := captureStdout(t, func() {
		runErr = runCLI([]string{"--format", "json", "--timeout", "50ms", "hello"})
	})
	if !errors.Is(runErr, context.DeadlineExceeded) || time.Since(start) > 3*time.Second {
		t.Fatalf("stdin timeout: %v", runErr)
	}
	events := decodeRunEvents(t, out)
	if len(events) != 1 || events[0]["type"] != "error" || !strings.Contains(events[0]["error"], "run timed out after 50ms") {
		t.Fatalf("timeout event: %s", out)
	}
}
