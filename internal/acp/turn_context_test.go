package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

func TestTurnContextKeepsValuesAndDeadline(t *testing.T) {
	f := newFixture(t, nil, nil, nil)
	type key struct{}
	parent, cancelParent := context.WithTimeout(context.WithValue(context.Background(), key{}, "trace"), 50*time.Millisecond)
	defer cancelParent()
	ctx, cancel := f.bridge.turnContext(parent)
	defer cancel()
	if ctx.Value(key{}) != "trace" {
		t.Fatal("request context values were lost")
	}
	want, _ := parent.Deadline()
	if got, ok := ctx.Deadline(); !ok || !got.Equal(want) {
		t.Fatalf("deadline = %v, %v; want %v", got, ok, want)
	}
	cancelParent()
	select {
	case <-ctx.Done():
		if ctx.Err() != context.DeadlineExceeded {
			t.Fatalf("SDK request cancellation interrupted turn: %v", ctx.Err())
		}
	case <-time.After(time.Second):
		t.Fatal("request deadline was ignored")
	}
}

func TestPromptCancellationStopsModel(t *testing.T) {
	for _, trigger := range []string{"disconnect", "deadline", "embedded-cancel"} {
		t.Run(trigger, func(t *testing.T) {
			started := make(chan struct{}, 1)
			stopped := make(chan struct{}, 1)
			release := make(chan struct{})
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n")
				w.(http.Flusher).Flush()
				started <- struct{}{}
				select {
				case <-r.Context().Done():
					stopped <- struct{}{}
				case <-release:
				}
			}))
			t.Cleanup(srv.Close)
			t.Cleanup(func() { close(release) })
			f := newFixture(t, nil, nil, factoryFor(srv, nil))
			f.initialize(t)
			id := f.newSession(t, t.TempDir())
			b := f.bridge
			if trigger == "embedded-cancel" {
				b = NewBridge("test", nil, nil, false, nil)
				b.sessions = map[acp.SessionId]*acpSession{id: f.bridge.getSession(id)}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if trigger == "deadline" {
				var stopDeadline context.CancelFunc
				ctx, stopDeadline = context.WithTimeout(ctx, time.Second)
				defer stopDeadline()
			}
			type result struct {
				resp acp.PromptResponse
				err  error
			}
			done := make(chan result, 1)
			go func() {
				resp, err := b.Prompt(ctx, acp.PromptRequest{SessionId: id, Prompt: []acp.ContentBlock{acp.TextBlock("start")}})
				done <- result{resp, err}
			}()
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("model request did not start")
			}
			switch trigger {
			case "disconnect":
				f.disconnect()
			case "embedded-cancel":
				cancel()
			}
			select {
			case res := <-done:
				if res.err != nil || res.resp.StopReason != acp.StopReasonCancelled {
					t.Fatalf("cancelled turn = %+v, %v", res.resp, res.err)
				}
			case <-time.After(3 * time.Second):
				_ = b.Cancel(context.Background(), acp.CancelNotification{SessionId: id})
				t.Fatal("turn survived cancellation")
			}
			select {
			case <-stopped:
			case <-time.After(3 * time.Second):
				t.Fatal("model HTTP request survived cancellation")
			}
		})
	}
}

func TestDisconnectStopsToolFromWirePrompt(t *testing.T) {
	started := make(chan struct{})
	stopped := make(chan struct{})
	tool := tools.Tool{
		Def: ai.NewTool("wait_disconnect", "Wait for cancellation", `{"type":"object","properties":{}}`),
		Run: func(ctx context.Context, _ json.RawMessage) (string, error) {
			close(started)
			<-ctx.Done()
			close(stopped)
			return "", ctx.Err()
		},
	}
	srv := scriptServer(t, []step{{toolName: "wait_disconnect", toolArgs: `{}`}})
	f := newFixture(t, nil, nil, factoryFor(srv, []tools.Tool{tool}))
	f.initialize(t)
	id := f.newSession(t, t.TempDir())
	done := make(chan struct{})
	go func() {
		_, _ = f.conn.Prompt(context.Background(), acp.PromptRequest{SessionId: id, Prompt: []acp.ContentBlock{acp.TextBlock("wait")}})
		close(done)
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("tool did not start")
	}
	f.disconnect()
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("tool survived editor disconnect")
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("wire request survived disconnect")
	}
}
