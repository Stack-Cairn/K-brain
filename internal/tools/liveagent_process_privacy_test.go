package tools

import (
	"fmt"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/privacy"
)

// Approval review redacts tool output with privacy.RedactText; nanosecond
// process IDs, PIDs and CreateTemp log names must not look like sensitive data.
func TestManagedProcessTextHasNoRedactionAcrossIDs(t *testing.T) {
	base := time.Now().UnixNano()
	hits := 0
	for i := int64(0); i < 20000; i++ {
		ns := base + i*7919*1_000_003
		snap := liveagentProcessSnapshot{
			ID:      fmt.Sprintf("proc-%d", ns),
			Status:  liveagentProcessRunning,
			PID:     int(1000 + i%60000),
			CWD:     `D:\Documents\Projects\Go\K-brain`,
			LogPath: fmt.Sprintf(`C:\Users\me\AppData\Local\Temp\k-brain-managed-process-%d.log`, uint32(ns)),
		}
		text := liveagentProcessText("wait", snap, "ok", i, i, false, true)
		if masked, changed := privacy.RedactText(text); changed {
			hits++
			if hits < 4 {
				t.Errorf("redacted: %q", masked)
			}
		}
	}
	if hits > 0 {
		t.Errorf("%d/20000 outputs redacted", hits)
	}
}
