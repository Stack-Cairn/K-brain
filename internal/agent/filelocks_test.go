package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

func TestFileLockCancellation(t *testing.T) {
	for _, kind := range []string{"path", "global"} {
		t.Run(kind, func(t *testing.T) {
			f := newFileLocks()
			acquire := f.acquireGlobal
			if kind == "path" {
				acquire = func(ctx context.Context) (func(), error) {
					return f.acquirePath(ctx, "same.go")
				}
			}
			release, err := acquire(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			if unlock, err := acquire(ctx); !errors.Is(err, context.DeadlineExceeded) || unlock != nil {
				t.Fatalf("waiting acquire = %v, unlock nil = %v", err, unlock == nil)
			}
			release()
			release()
			ctx, cancel = context.WithCancel(context.Background())
			cancel()
			if unlock, err := acquire(ctx); !errors.Is(err, context.Canceled) || unlock != nil {
				t.Fatalf("cancelled acquire = %v, unlock nil = %v", err, unlock == nil)
			}
			unlock, err := acquire(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			unlock()
			if len(f.locks) != 0 {
				t.Fatalf("unused locks were retained: %d", len(f.locks))
			}
		})
	}
}

func TestPathLockConcurrentReclamation(t *testing.T) {
	f := newFileLocks()
	var active atomic.Int32
	var wg sync.WaitGroup
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for range 20 {
		wg.Go(func() {
			for range 100 {
				release, err := f.acquirePath(ctx, "same.go")
				if err != nil {
					t.Error(err)
					return
				}
				if active.Add(1) != 1 {
					t.Error("same-path critical sections overlapped")
				}
				active.Add(-1)
				release()
			}
		})
	}
	wg.Wait()
	if len(f.locks) != 0 {
		t.Fatalf("unused locks were retained: %d", len(f.locks))
	}
}

func TestRunToolsCancelsWhileWaitingForLock(t *testing.T) {
	for _, name := range []string{"write", "bash"} {
		t.Run(name, func(t *testing.T) {
			a := New(ai.New("http://unused", "k"), "m", 100, "sys")
			var called atomic.Bool
			a.Tools = []tools.Tool{{
				Def: ai.NewTool(name, "test", `{"type":"object"}`),
				Run: func(context.Context, json.RawMessage) (string, error) {
					called.Store(true)
					return "unexpected execution", nil
				},
			}}
			var release func()
			var err error
			if name == "write" {
				release, err = a.files.acquirePath(context.Background(), "same.go")
			} else {
				release, err = a.files.acquireGlobal(context.Background())
			}
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			call := ai.ToolCall{ID: "locked"}
			call.Function.Name = name
			call.Function.Arguments = `{"path":"same.go"}`
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			done := make(chan []tools.Result, 1)
			go func() { done <- a.runTools(ctx, []ai.ToolCall{call}, Events{}) }()
			select {
			case results := <-done:
				if len(results) != 1 || !strings.Contains(results[0].Text, context.DeadlineExceeded.Error()) {
					t.Fatalf("results = %v", results)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("tool waited for the lock after cancellation")
			}
			if called.Load() {
				t.Fatal("cancelled tool executed")
			}
		})
	}
}
