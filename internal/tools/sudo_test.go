package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestBashSudoFastFail(t *testing.T) {
	if out := run(t, "bash", `{"command":"command -v sudo >/dev/null || echo MISSING","timeout":5}`); strings.Contains(out, "MISSING") {
		t.Skip("sudo not installed; skipping end-to-end sudo hang test")
	}

	start := time.Now()
	out := Execute(context.Background(), All(), "bash", json.RawMessage(`{"command":"sudo true 2>&1; echo RC=$?","timeout":5}`))
	elapsed := time.Since(start)

	if elapsed > 4*time.Second {
		t.Fatalf("sudo invocation hung %s — the /dev/tty hang regressed: %q", elapsed, out)
	}

	if strings.Contains(out, "command timed out") {
		t.Fatalf("sudo call timed out — fast-fail regressed: %q", out)
	}
}
