package lsp

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestSpawnDedup(t *testing.T) {
	m := NewManager(map[string]ServerSpec{"gopls": fakeSpec()})
	defer m.Close()
	dir := t.TempDir()
	writeFile(t, dir+"/go.mod", "module x\n")
	for i := range 8 {
		writeFile(t, fmt.Sprintf("%s/f%d.go", dir, i), "package main\n")
	}

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			m.WaitDiagnostics(context.Background(), fmt.Sprintf("%s/f%d.go", dir, i))
		})
	}
	wg.Wait()

	m.mu.Lock()
	defer m.mu.Unlock()
	if n := len(m.broken); n != 1 {
		t.Fatalf("expected exactly 1 spawn attempt (1 broken entry), got %d", n)
	}
	if len(m.spawning) != 0 {
		t.Fatalf("spawning map leaked: %v", m.spawning)
	}
}

func TestParallelWaiterWake(t *testing.T) {
	var pushes sync.WaitGroup
	pushes.Add(2)
	f := startFakeServer(t, func(uri string, version int) []push {
		pushes.Done()
		return []push{{version: version, diags: []Diagnostic{
			{Line: 1, Col: 1, Severity: SeverityError, Message: "boom " + uri},
		}}}
	})
	m := pipeManager(f)
	defer m.Close()

	dir := t.TempDir()
	writeFile(t, dir+"/a.go", "package main\n")
	writeFile(t, dir+"/b.go", "package main\n")

	before := numGoroutines()

	var wg sync.WaitGroup
	outs := make([]string, 2)
	for i, name := range []string{"a.go", "b.go"} {
		wg.Go(func() {
			outs[i] = m.WaitDiagnostics(context.Background(), dir+"/"+name)
		})
	}
	wg.Wait()
	pushes.Wait()

	for i, out := range outs {
		if out == "" {
			t.Fatalf("waiter %d got no diagnostics", i)
		}
	}

	time.Sleep(50 * time.Millisecond)
	if after := numGoroutines(); after > before+2 {
		t.Fatalf("goroutines grew: before=%d after=%d", before, after)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.waiters) != 0 {
		t.Fatalf("waiters leaked: %v", m.waiters)
	}
}

func TestWaitBeforePublishInterleaving(t *testing.T) {
	f := startFakeServer(t, func(uri string, version int) []push {
		return []push{{version: version, diags: []Diagnostic{
			{Line: 5, Col: 5, Severity: SeverityError, Message: "late"},
		}}}
	})
	m := pipeManager(f)
	defer m.Close()

	dir := t.TempDir()
	writeFile(t, dir+"/main.go", "package main\n")

	m.WaitDiagnostics(context.Background(), dir+"/main.go")

	writeFile(t, dir+"/main.go", "package main\n\n")
	out := m.WaitDiagnostics(context.Background(), dir+"/main.go")
	if out == "" {
		t.Fatal("interleaved push was lost")
	}
}
