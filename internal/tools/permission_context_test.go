package tools

import (
	"context"
	"strings"
	"sync"
	"testing"
)

func TestPermissionGateContextIsolation(t *testing.T) {
	previous := Gate
	Gate = func(GateRequest) (GateDecision, string) { return GateReject, "global" }
	t.Cleanup(func() { Gate = previous })
	base := context.Background()
	allow := WithGate(base, nil)
	deny := WithGate(base, func(req GateRequest) (GateDecision, string) {
		if req.Context == nil || req.Tool != "bash" || req.Rule != "git status" {
			t.Errorf("gate request = %+v", req)
		}
		return GateReject, "scoped"
	})
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if err := Authorize(allow, "bash", "git status --short"); err != nil {
				t.Error(err)
			}
			if err := Authorize(deny, "bash", "git status --short"); err == nil || !strings.Contains(err.Error(), "scoped") {
				t.Errorf("scoped gate = %v", err)
			}
			if err := Authorize(base, "bash", "git status --short"); err == nil || !strings.Contains(err.Error(), "global") {
				t.Errorf("fallback gate = %v", err)
			}
		})
	}
	wg.Wait()
}
