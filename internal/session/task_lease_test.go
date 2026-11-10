package session

import (
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

// kn 保存的运行中子代理，在另一个进程看来：租约持有期间仍在运行，关闭后就不是了。
func TestTaskLeaseMarksRunningElsewhere(t *testing.T) {
	kn, backend := openSharedStores(t)
	id, err := kn.Create(t.TempDir(), "m", "p")
	if err != nil {
		t.Fatal(err)
	}
	token, err := kn.TaskLease(id)
	if err != nil {
		t.Fatal(err)
	}
	again, err := kn.TaskLease(id)
	if err != nil || again != token {
		t.Fatalf("second lease = %q, %v; want the same %q", again, err, token)
	}
	usage := ai.Usage{PromptTokens: 7, CompletionTokens: 3}
	task := Task{ID: "survey-1", Description: "Survey", Status: "running", StartedAt: time.Now(), Foreground: true, ToolCount: 2, Usage: &usage, Lease: token}
	if err := kn.SaveTask(id, task); err != nil {
		t.Fatal(err)
	}
	stored, err := backend.LoadTasks(id)
	if err != nil || len(stored) != 1 {
		t.Fatalf("LoadTasks = %+v, %v", stored, err)
	}
	got := stored[0]
	if !got.Foreground || got.ToolCount != 2 || got.Usage == nil || got.Usage.PromptTokens != 7 || got.Lease != token {
		t.Fatalf("stored task lost progress fields: %+v", got)
	}
	if !backend.TaskRunningElsewhere(got) {
		t.Fatal("backend does not see the kn subagent running")
	}
	if kn.TaskRunningElsewhere(got) {
		t.Fatal("the owner sees its own subagent as running elsewhere")
	}
	done := got
	done.Status = "done"
	if backend.TaskRunningElsewhere(done) {
		t.Fatal("a finished subagent is reported running")
	}
	if err := kn.Close(); err != nil {
		t.Fatal(err)
	}
	if backend.TaskRunningElsewhere(got) || backend.TaskLeaseLive(token) {
		t.Fatal("subagent still reported running after its process closed the store")
	}
}

func TestTaskLeaseRejectsBadTokens(t *testing.T) {
	a, _ := openSharedStores(t)
	for _, token := range []string{"", "../x", "a/b", `a\b`, ".hidden"} {
		if a.TaskLeaseLive(token) {
			t.Fatalf("token %q reported live", token)
		}
	}
	if _, err := a.TaskLease("../nope"); err == nil {
		t.Fatal("lease for an invalid session id")
	}
}
