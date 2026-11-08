package stackdump

import (
	"context"
	"fmt"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

// Watchdog runs Probe every Interval. When a probe has not returned after
// Timeout, it writes one dump for that stall and logs a warning; the next
// stall, after a probe has returned again, may dump again, up to MaxDumps.
type Watchdog struct {
	Name     string
	Interval time.Duration
	Timeout  time.Duration
	// Probe returns once the watched loop or lock has made progress. It must
	// return when ctx is cancelled.
	Probe func(ctx context.Context)
	// Paused, when set, reports a state in which the watched loop is blocked
	// on purpose (an external editor in the foreground); time spent paused
	// does not count.
	Paused func() bool
	// MaxDumps bounds the dumps per process; 0 means 3.
	MaxDumps int
	// OnStall is called after a stall's dump with its outcome (tests, logs).
	OnStall func(path string, err error)
	// Write writes the dump; nil uses stackdump.Write.
	Write func(reason string) (string, error)
}

// Start runs the watchdog until ctx is cancelled.
func (w *Watchdog) Start(ctx context.Context) {
	go w.run(ctx)
}

func (w *Watchdog) run(ctx context.Context) {
	interval := w.Interval
	if interval <= 0 {
		interval = 2 * time.Second
	}
	maxDumps := w.MaxDumps
	if maxDumps <= 0 {
		maxDumps = 3
	}
	write := w.Write
	if write == nil {
		write = Write
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var (
		inFlight <-chan struct{}
		waited   time.Duration
		dumped   bool
		dumps    int
		last     = time.Now()
	)
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			step := now.Sub(last)
			last = now
			if step > 3*interval {
				// The process itself was not scheduled (machine asleep, the
				// process stopped): not the loop's fault.
				step = interval
			}
			if inFlight != nil {
				select {
				case <-inFlight:
					inFlight, waited, dumped = nil, 0, false
				default:
				}
			}
			if inFlight == nil {
				done := make(chan struct{})
				inFlight = done
				go func() {
					defer close(done)
					w.Probe(ctx)
				}()
				continue
			}
			if w.Paused != nil && w.Paused() {
				continue
			}
			waited += step
			if waited < w.Timeout || dumped || dumps >= maxDumps {
				continue
			}
			dumped = true
			dumps++
			reason := fmt.Sprintf("watchdog: %s made no progress for %s", w.Name, waited.Round(time.Second))
			path, err := write(reason)
			if err != nil {
				config.LogEvent("watchdog", fmt.Sprintf("WARNING %s; stack dump failed: %v", reason, err))
			} else {
				config.LogEvent("watchdog", fmt.Sprintf("WARNING %s; stacks in %s", reason, path))
			}
			if w.OnStall != nil {
				w.OnStall(path, err)
			}
		}
	}
}
