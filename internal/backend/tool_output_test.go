package backend

import (
	"encoding/json"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Stack-Cairn/K-brain/internal/protocol"
)

type toolOutputRecorder struct {
	mu   sync.Mutex
	sent []string
}

func (r *toolOutputRecorder) publish(id, output string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, id+":"+output)
}

func (r *toolOutputRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.sent...)
}

func TestToolOutputThrottleCoalescesAndFlushesTrailingSnapshot(t *testing.T) {
	var rec toolOutputRecorder
	th := newToolOutputThrottle(60*time.Millisecond, rec.publish)
	th.update("a", "1")
	th.update("a", "12")
	th.update("a", "123")
	if got := rec.snapshot(); len(got) != 1 || got[0] != "a:1" {
		t.Fatalf("first snapshot must go out immediately and later ones wait: %v", got)
	}
	// 输出停住后，尾随发布必须送达最后一份快照。
	deadline := time.Now().Add(2 * time.Second)
	for len(rec.snapshot()) < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := rec.snapshot(); len(got) != 2 || got[1] != "a:123" {
		t.Fatalf("trailing snapshot missing or not coalesced: %v", got)
	}
}

func TestToolOutputThrottleDropsPendingAfterFinish(t *testing.T) {
	var rec toolOutputRecorder
	th := newToolOutputThrottle(40*time.Millisecond, rec.publish)
	th.update("a", "first")
	th.update("a", "pending")
	th.finish("a")
	th.update("a", "late")
	th.update("b", "other")
	time.Sleep(120 * time.Millisecond)
	got := rec.snapshot()
	if len(got) != 2 || got[0] != "a:first" || got[1] != "b:other" {
		t.Fatalf("finished call must not publish again: %v", got)
	}
	th.update("b", "more")
	th.update("b", "pending")
	th.close()
	th.update("b", "after-close")
	time.Sleep(80 * time.Millisecond)
	if got := rec.snapshot(); len(got) != 3 || got[2] != "b:more" {
		t.Fatalf("closed throttle published a pending or later snapshot: %v", got)
	}
}

func TestTailToolOutputBoundsAtLineAndRuneBoundaries(t *testing.T) {
	if got := tailToolOutput("short", 16); got != "short" {
		t.Fatalf("short output changed: %q", got)
	}
	lines := strings.Repeat("line-xxxxxxxxx\n", 100)
	got := tailToolOutput(lines, 64)
	if len(got) > 64 || !strings.HasPrefix(got, "line-") {
		t.Fatalf("tail should start on a whole line within the cap: %q", got)
	}
	noBreaks := strings.Repeat("编译", 100)
	got = tailToolOutput(noBreaks, 31)
	if len(got) > 31 || !utf8.ValidString(got) {
		t.Fatalf("tail split a rune or exceeded cap: %d %q", len(got), got)
	}
}

// 桌面端 Bash 运行中要通过 tool.status 推送输出尾部，且全部排在 tool.result 之前（#975）。
func TestRunStreamsLiveAgentBashOutputBeforeResult(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix shell fixture")
	}
	t.Setenv("K_BRAIN_SHELL", "bash")
	client := &cwdClient{args: map[string]any{"command": "echo compiling-step-1; sleep 1.2; echo compiling-step-2"}}
	base, _ := newRulesServer(t, client)
	sess := createTestSession(t, base)
	events, _ := runRules(t, base, sess.ID, "full_access", func(protocol.PermissionRequest) protocol.PermissionDecision {
		return protocol.PermissionDecision{Decision: "allow_once"}
	})
	sawLive, sawResult := false, false
	for _, event := range events {
		switch event.Type {
		case protocol.EventToolStatus:
			var status protocol.ToolStatus
			if err := json.Unmarshal(event.Payload, &status); err != nil {
				t.Fatal(err)
			}
			if status.ToolCallID != "cwd-call-1" {
				continue
			}
			if sawResult {
				t.Fatalf("live output arrived after tool.result: %+v", status)
			}
			if status.Status != "running" || len(status.Message) > maxToolOutputBytes {
				t.Fatalf("bad live status: %+v", status)
			}
			if strings.Contains(status.Message, "compiling-step-1") {
				sawLive = true
			}
		case protocol.EventToolResult:
			sawResult = true
		}
	}
	if !sawLive || !sawResult {
		t.Fatalf("live=%v result=%v events=%d", sawLive, sawResult, len(events))
	}
}
