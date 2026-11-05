package backend

import (
	"context"
	"io/fs"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
)

// lingeringSubagentClient is bgSubagentClient whose background subagent, once
// cancelled, takes until linger closes to return, like a tool unwinding.
type lingeringSubagentClient struct {
	*bgSubagentClient
	started   chan struct{}
	cancelled chan struct{}
	linger    chan struct{}
	once      sync.Once
}

func (c *lingeringSubagentClient) Clone() ai.Client { return c }

func (c *lingeringSubagentClient) Stream(ctx context.Context, request ai.Request, onText, onThink func(string), onCall func(string, string, string)) (ai.Message, ai.Usage, error) {
	if !isBackgroundSubagentRequest(request) {
		return c.bgSubagentClient.Stream(ctx, request, onText, onThink, onCall)
	}
	c.once.Do(func() { close(c.started) })
	<-ctx.Done()
	close(c.cancelled)
	<-c.linger
	return ai.Message{}, ai.Usage{}, ctx.Err()
}

// eventDirState lists every file under dir with its size and modification time.
func eventDirState(t *testing.T, dir string) map[string][2]int64 {
	t.Helper()
	state := map[string][2]int64{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		state[path] = [2]int64{info.Size(), info.ModTime().UnixNano()}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return state
}

// Close ends the background subagents still working and returns only after they
// stopped writing: the owner closes the store and removes directories next, and a
// subagent settling after that wrote its task, transcript and journal events into
// them ("directory not empty" on test cleanup, writes to a closed store in the app).
func TestCloseCancelsAndWaitsForBackgroundSubagents(t *testing.T) {
	client := &lingeringSubagentClient{
		bgSubagentClient: &bgSubagentClient{release: make(chan struct{})},
		started:          make(chan struct{}), cancelled: make(chan struct{}), linger: make(chan struct{}),
	}
	base, rt, srv := newHandoffServerWith(t, client)
	startHandoffRun(t, base, rt)
	waitRunTerminal(t, rt, "user-run")
	task := backgroundTask(t, rt)
	select {
	case <-client.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the background subagent made no request")
	}

	closed := make(chan error, 1)
	go func() { closed <- srv.Close() }()
	select {
	case <-client.cancelled:
	case <-time.After(5 * time.Second):
		close(client.linger)
		t.Fatal("Close did not cancel the background subagent")
	}
	select {
	case err := <-closed:
		close(client.linger)
		t.Fatalf("Close returned (%v) while the background subagent was still running", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(client.linger)
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return after the background subagent ended")
	}

	before := eventDirState(t, srv.eventDir)
	time.Sleep(200 * time.Millisecond)
	if after := eventDirState(t, srv.eventDir); !reflect.DeepEqual(before, after) {
		t.Fatalf("event dir changed after Close returned:\nbefore %v\nafter  %v", before, after)
	}
	stored, err := srv.store.LoadTasks(rt.id)
	if err != nil {
		t.Fatal(err)
	}
	// Shutdown is not the user's cancel: the task stays running in the store, and
	// the next start reports it interrupted like after any exit.
	if len(stored) != 1 || stored[0].ID != task.ID || stored[0].Status != string(agent.TaskRunning) {
		t.Fatalf("stored tasks after Close = %+v", stored)
	}
}
