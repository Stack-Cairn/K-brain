package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Stack-Cairn/K-brain/internal/stackdump"
)

// uiWatchdogTimeout is how long the event loop may go without handling a
// message (Update plus the View after it) before a stack dump is written.
const uiWatchdogTimeout = 20 * time.Second

// watchdogPing is answered by Update as soon as the event loop reaches it.
type watchdogPing struct{ done chan struct{} }

// uiWatchdog pings the event loop through send. Keys and mouse events queue
// behind the same loop, so a ping that is not answered means typed input is
// not being handled either: the UI is frozen. Time with an external editor in
// the foreground does not count.
func uiWatchdog(send func(tea.Msg), timeout time.Duration) *stackdump.Watchdog {
	return &stackdump.Watchdog{
		Name:     "TUI event loop",
		Interval: 2 * time.Second,
		Timeout:  timeout,
		Paused:   externalProgramRunning.Load,
		Probe: func(ctx context.Context) {
			done := make(chan struct{})
			go send(watchdogPing{done: done})
			select {
			case <-done:
			case <-ctx.Done():
			}
		},
	}
}

// startDiagnostics lets a frozen UI be inspected: SIGUSR1 (`kn debug stacks
// <pid>`) writes every goroutine's stack next to k-brain.log, and the
// watchdog does so by itself when the event loop stalls. Dumps are logged to
// k-brain.log; nothing is printed over the full-screen UI.
func startDiagnostics(p *tea.Program) (stop func()) {
	stopSignal := stackdump.OnSignal(nil)
	ctx, cancel := context.WithCancel(context.Background())
	uiWatchdog(p.Send, uiWatchdogTimeout).Start(ctx) //nolint:uilock // Send runs on the probe's own goroutine
	return func() {
		cancel()
		stopSignal()
	}
}
