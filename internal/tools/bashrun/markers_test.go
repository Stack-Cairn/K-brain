package bashrun

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestChildMarkers(t *testing.T) {
	previous := ChildMarkers
	t.Cleanup(func() { ChildMarkers = previous })
	SetMarkers("sess123", "model1")
	command := `printf 'K_BRAIN=%s\nK_BRAIN_SESSION_ID=%s\nK_BRAIN_MODEL=%s\nK_BRAIN_PID=%s\n' "$K_BRAIN" "$K_BRAIN_SESSION_ID" "$K_BRAIN_MODEL" "$K_BRAIN_PID"`
	if runtime.GOOS == "windows" {
		command = `Get-ChildItem Env:K_BRAIN,Env:K_BRAIN_SESSION_ID,Env:K_BRAIN_MODEL,Env:K_BRAIN_PID | ForEach-Object { "$($_.Name)=$($_.Value)" }`
	}
	res := Run(context.Background(), Options{Command: command, Timeout: 5 * time.Second})
	for _, want := range []string{"K_BRAIN=1", "K_BRAIN_SESSION_ID=sess123", "K_BRAIN_MODEL=model1", "K_BRAIN_PID="} {
		if !strings.Contains(res.Output, want) {
			t.Fatalf("child env missing %q:\n%s", want, res.Output)
		}
	}
}
