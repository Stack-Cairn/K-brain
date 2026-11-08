//go:build !windows

package stackdump

import (
	"os"
	"os/signal"
	"syscall"
)

// SignalSupported reports whether OnSignal can dump on a signal here.
const SignalSupported = true

// OnSignal writes a dump each time the process receives SIGUSR1 and calls
// report with the outcome (report may be nil). The process keeps running.
// The returned function stops listening.
func OnSignal(report func(path string, err error)) (stop func()) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGUSR1)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-ch:
				path, err := Write("SIGUSR1")
				if report != nil {
					report(path, err)
				}
			case <-done:
				return
			}
		}
	}()
	return func() {
		signal.Stop(ch)
		close(done)
	}
}

// Signal asks process pid to write a dump.
func Signal(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Signal(syscall.SIGUSR1)
}
