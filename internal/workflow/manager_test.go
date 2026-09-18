package workflow

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestManagerStartRejectsBadRunID(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	m := NewManager(echoRunner(nil), "")
	for _, bad := range []string{"../../foo", "..", "a/b", "a b"} {
		if _, err := m.Start(metaHeader+"return await agent('x')", nil, bad); err == nil {
			t.Errorf("Start(bad=%q) succeeded, want error", bad)
		}
	}
}

func TestManagerResumeLoadsJournalBeforeSave(t *testing.T) {
	home := t.TempDir()
	t.Setenv("K_BRAIN_HOME", home)
	m := NewManager(echoRunner(nil), "")

	script := metaHeader + `const a = await agent('once')` + "\nreturn a"
	r1, err := m.Start(script, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	waitSettled(t, m, r1.ID)

	var calls atomic.Int64
	m.runner = echoRunner(&calls)

	r2, err := m.Start(script, nil, r1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if r2.ID != r1.ID {
		t.Fatalf("resume reused runID: got %q want %q", r2.ID, r1.ID)
	}
	waitSettled(t, m, r2.ID)
	if calls.Load() != 0 {
		t.Fatalf("resume re-ran %d agent calls; journal should have replayed them", calls.Load())
	}
}

func TestManagerResumePersistsFullJournal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("K_BRAIN_HOME", home)
	m := NewManager(echoRunner(nil), "")

	script := metaHeader + `
const a = await agent('first')
const b = await agent('second')
return [a, b].join('|')
`
	r1, err := m.Start(script, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	waitSettled(t, m, r1.ID)

	var calls atomic.Int64
	m.runner = echoRunner(&calls)
	_, err = m.Start(script, nil, r1.ID)
	if err != nil {
		t.Fatal(err)
	}

	waitSettled(t, m, r1.ID)
	if calls.Load() != 0 {
		t.Fatalf("first resume re-ran %d calls, want 0", calls.Load())
	}
	persisted := LoadRun(r1.ID)
	if len(persisted.Journal) != 2 {
		t.Fatalf("after resume, persisted journal has %d entries, want 2 (replayed prefix + live)", len(persisted.Journal))
	}

	calls.Store(0)
	_, err = m.Start(script, nil, r1.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitSettled(t, m, r1.ID)
	if calls.Load() != 0 {
		t.Fatalf("second resume re-ran %d calls; the persisted prefix was dropped", calls.Load())
	}
}

func TestManagerRejectsResumeOfRunningRun(t *testing.T) {
	home := t.TempDir()
	t.Setenv("K_BRAIN_HOME", home)
	release := make(chan struct{})

	m := NewManager(func(ctx context.Context, _ AgentRequest) (any, Usage, error) {
		select {
		case <-ctx.Done():
			return nil, Usage{}, ctx.Err()
		case <-release:
			return "ok", Usage{}, nil
		}
	}, "")
	r, err := m.Start(metaHeader+"return await agent('x')", nil, "")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := m.Start(metaHeader+"return await agent('x')", nil, r.ID); err == nil ||
		!strings.Contains(err.Error(), "still running") {
		t.Fatalf("expected 'still running' error, got %v", err)
	}
	close(release)
	waitSettled(t, m, r.ID)
}

func TestManagerStopCancelsRun(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	release := make(chan struct{})
	m := NewManager(func(ctx context.Context, _ AgentRequest) (any, Usage, error) {
		select {
		case <-ctx.Done():
			return nil, Usage{}, ctx.Err()
		case <-release:
			return "ok", Usage{}, nil
		}
	}, "")
	r, err := m.Start(metaHeader+"return await agent('x')", nil, "")
	if err != nil {
		t.Fatal(err)
	}

	time.Sleep(50 * time.Millisecond)

	if m.Stop("nope") {
		t.Error("Stop(unknown) = true, want false")
	}

	if !m.Stop(r.ID) {
		t.Fatal("Stop(running) = false, want true")
	}
	waitSettled(t, m, r.ID)

	if m.Stop(r.ID) {
		t.Error("Stop(settled) = true, want false")
	}
	close(release)
}

func TestManagerScriptError(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	m := NewManager(echoRunner(nil), "")
	r, err := m.Start(metaHeader+"throw new Error('boom')", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	waitSettled(t, m, r.ID)
	snap, ok := m.Snapshot(r.ID)
	if !ok {
		t.Fatalf("Snapshot missing for %s", r.ID)
	}
	if snap.Status != RunError {
		t.Fatalf("Status = %q, want error", snap.Status)
	}

	if snap.Error == "" {
		t.Fatal("snapshot Error is empty, want the thrown error surfaced")
	}
}

func TestManagerPhasedRun(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	m := NewManager(echoRunner(nil), "")
	script := `export const meta = {
  name: 'phased', description: 'd',
  phases: [{ title: 'survey' }, { title: 'build' }],
}
phase('survey')
const a = await agent('look')
phase('build')
const b = await agent('make')
return [a, b].join('|')`
	r, err := m.Start(script, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	waitSettled(t, m, r.ID)
	snap, ok := m.Snapshot(r.ID)
	if !ok {
		t.Fatalf("Snapshot missing for %s", r.ID)
	}
	if len(snap.Agents) != 2 {
		t.Fatalf("Snapshot has %d agents, want 2", len(snap.Agents))
	}

	if len(snap.Phases) != 2 || snap.Phases[0] != "survey" || snap.Phases[1] != "build" {
		t.Fatalf("Snapshot phases = %v, want [survey build]", snap.Phases)
	}
}

func TestManagerListReturnsSnapshots(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	m := NewManager(echoRunner(nil), "")
	r, err := m.Start(metaHeader+"return await agent('x')", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	waitSettled(t, m, r.ID)

	runs := m.List()
	if len(runs) != 1 {
		t.Fatalf("List returned %d runs, want 1", len(runs))
	}
	if runs[0].ID != r.ID {
		t.Fatalf("List[0].ID = %q want %q", runs[0].ID, r.ID)
	}
	if runs[0].Status != RunComplete {
		t.Fatalf("List[0].Status = %q want complete", runs[0].Status)
	}
	snap, ok := m.Snapshot(r.ID)
	if !ok {
		t.Fatalf("Snapshot missing for %s", r.ID)
	}
	if len(snap.Agents) != 1 {
		t.Fatalf("Snapshot has %d agents, want 1", len(snap.Agents))
	}
}

func waitSettled(t *testing.T, m *Manager, id string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r := m.get(id)
		if r == nil {
			t.Fatalf("run %s vanished", id)
		}
		select {
		case <-r.Done:
			return
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("run %s did not settle", id)
}
