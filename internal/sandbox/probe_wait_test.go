package sandbox

import (
	"testing"
	"time"
)

// A probe whose confined launch never returns must not hold up callers that
// need an answer now (/v1/health), and must not stop a later Reprobe.
func TestProbeWithinDoesNotWaitForAWedgedProbe(t *testing.T) {
	release := make(chan struct{})
	oldFunc := probeFunc
	probeFunc = func() Capability {
		<-release
		return Capability{Mechanism: "wedged"}
	}
	Reprobe()
	t.Cleanup(func() {
		close(release)
		probeFunc = oldFunc
		Reprobe()
	})

	for range 3 {
		start := time.Now()
		if _, ok := ProbeWithin(50 * time.Millisecond); ok {
			t.Fatal("a wedged probe cannot have an answer")
		}
		if waited := time.Since(start); waited > 2*time.Second {
			t.Fatalf("ProbeWithin waited %v for a wedged probe", waited)
		}
	}

	probeFunc = func() Capability { return Capability{Mechanism: "fresh", Supported: true} }
	Reprobe()
	c, ok := ProbeWithin(5 * time.Second)
	if !ok || c.Mechanism != "fresh" {
		t.Fatalf("after Reprobe a new probe must run, got %+v ok=%v", c, ok)
	}
	if got := Probe(); got.Mechanism != "fresh" {
		t.Fatalf("Probe = %+v, want the fresh capability", got)
	}
}
