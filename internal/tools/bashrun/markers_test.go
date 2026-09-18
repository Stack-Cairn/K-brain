package bashrun

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestChildMarkers(t *testing.T) {
	SetMarkers("sess123", "kimi-k3-fast")
	res := Run(context.Background(), Options{Command: "env", Timeout: 5 * time.Second})
	for _, want := range []string{"K_BRAIN=1", "K_BRAIN_SESSION_ID=sess123", "K_BRAIN_MODEL=kimi-k3-fast", "K_BRAIN_PID="} {
		if !strings.Contains(res.Output, want) {
			t.Fatalf("child env missing %q:\n%s", want, res.Output)
		}
	}
}
