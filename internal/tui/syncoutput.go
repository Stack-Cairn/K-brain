package tui

// Synchronized output (DEC private mode 2026).
//
// bubbletea's renderer builds each frame in memory and hands it to the
// program's output in a single Write. A terminal that reads that write in
// several chunks can show a half-drawn frame for one refresh: the composer
// jumps, a status row flickers. Terminals that implement mode 2026 hold
// their screen while it is set, so kn brackets every write of the renderer in
// BSU ("\x1b[?2026h") and ESU ("\x1b[?2026l") and the terminal presents the
// frame in one piece. The bracketing lives in syncOutput, an io.Writer around
// the program's output; nothing in bubbletea changes.
//
// Whether to do it is decided once at startup (detectSyncOutput):
//
//   - K_BRAIN_SYNC_OUTPUT=1 / 0 forces it on or off.
//   - Inside tmux it is on from tmux 3.4, which implements the mode itself
//     and redraws the pane atomically; the sequences go to tmux as they are,
//     never wrapped in a passthrough to the outer terminal. GNU screen and
//     older tmux get none.
//   - Otherwise kn asks the terminal with DECRQM ("\x1b[?2026$p") followed
//     by a primary device attributes query, which every terminal answers:
//     a mode report of set or reset means supported; DA1 arriving first, or
//     no answer in time, means not.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const (
	syncBegin = "\x1b[?2026h"
	syncEnd   = "\x1b[?2026l"
	// syncQuery asks for mode 2026's state, then for the device attributes
	// every terminal reports, so the reader knows when to stop waiting.
	syncQuery = "\x1b[?2026$p\x1b[c"
)

// syncOutput is the program's output with each write bracketed in BSU/ESU
// while on. It embeds the terminal's *os.File so bubbletea still sees a TTY
// (size queries, raw mode) through it.
type syncOutput struct {
	*os.File
	on atomic.Bool
}

func newSyncOutput(f *os.File, on bool) *syncOutput {
	w := &syncOutput{File: f}
	w.on.Store(on)
	return w
}

// Write sends p as one write, bracketed when synchronized output is on. It
// reports len(p) on success so callers never see the added bytes.
func (w *syncOutput) Write(p []byte) (int, error) {
	if len(p) == 0 || !w.on.Load() {
		return w.File.Write(p)
	}
	buf := make([]byte, 0, len(syncBegin)+len(p)+len(syncEnd))
	buf = append(buf, syncBegin...)
	buf = append(buf, p...)
	buf = append(buf, syncEnd...)
	if _, err := w.File.Write(buf); err != nil {
		return 0, err
	}
	return len(p), nil
}

// plainOutput is w without the bracketing: what a child process that takes
// over the terminal (an external editor) must get as its stdout, so it sees
// the terminal itself instead of a pipe.
func plainOutput(w any) any {
	if so, ok := w.(*syncOutput); ok {
		return so.File
	}
	return w
}

// syncState is the startup decision, kept for /doctor.
var syncState struct {
	on  bool
	how string
}

// envSwitch reads a 1/0-style override: set reports whether v says anything.
func envSwitch(v string) (on, set bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "on", "true", "yes":
		return true, true
	case "0", "off", "false", "no":
		return false, true
	}
	return false, false
}

// Seams for tests.
var (
	tmuxVersion   = readTmuxVersion
	syncTermQuery = querySyncOutputTTY
)

// detectSyncOutput decides whether frames are bracketed and records why.
// It must run before bubbletea starts reading the terminal, since it reads
// the terminal's answer itself.
func detectSyncOutput() bool {
	on, how := decideSyncOutput(os.Getenv)
	syncState.on, syncState.how = on, how
	return on
}

func decideSyncOutput(getenv func(string) string) (bool, string) {
	if on, set := envSwitch(getenv("K_BRAIN_SYNC_OUTPUT")); set {
		return on, "K_BRAIN_SYNC_OUTPUT=" + getenv("K_BRAIN_SYNC_OUTPUT")
	}
	if getenv("TMUX") != "" {
		v := tmuxVersion()
		if v == "" {
			return false, "tmux version unknown"
		}
		if tmuxAtLeast(v, 3, 4) {
			return true, "tmux " + v + " handles it"
		}
		return false, "tmux " + v + " (needs 3.4 or later)"
	}
	if t := getenv("TERM"); strings.HasPrefix(t, "screen") || strings.HasPrefix(t, "tmux") {
		return false, "not supported by " + t
	}
	if inMoshEnv() {
		return false, "not supported over mosh"
	}
	supported, answered := syncTermQuery()
	switch {
	case supported:
		return true, "terminal reports mode 2026"
	case answered:
		return false, "terminal does not support mode 2026"
	}
	return false, "no answer to the mode query"
}

// syncReportRE is a DECRPM answer for mode 2026; the group is its state.
var (
	syncReportRE = regexp.MustCompile(`\x1b\[\?2026;([0-9])\$y`)
	da1RE        = regexp.MustCompile(`\x1b\[\?[0-9;]*c`)
)

// parseSyncReply reads what the terminal sent back to syncQuery. done says
// the answer is complete; supported is only meaningful then.
func parseSyncReply(s string) (supported, done bool) {
	if m := syncReportRE.FindStringSubmatch(s); m != nil {
		// 1 set, 2 reset: the terminal implements the mode. 3 (permanently
		// set) is harmless to bracket; 0 (unknown) and 4 (permanently reset)
		// are not support.
		switch m[1] {
		case "1", "2", "3":
			return true, true
		}
		return false, true
	}
	if da1RE.MatchString(s) {
		return false, true
	}
	return false, false
}

func readTmuxVersion() string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "tmux", "display-message", "-p", "#{version}").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// tmuxAtLeast compares a tmux version string ("3.4", "3.3a", "next-3.5")
// with major.minor.
func tmuxAtLeast(v string, major, minor int) bool {
	v = strings.TrimPrefix(v, "next-")
	maj, rest, _ := strings.Cut(v, ".")
	mj, err := strconv.Atoi(maj)
	if err != nil {
		return false
	}
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	mn, _ := strconv.Atoi(rest[:end])
	return mj > major || (mj == major && mn >= minor)
}

// syncDoctor is /doctor's synchronized-output row.
func syncDoctor() string {
	how := syncState.how
	if how == "" {
		how = "not checked"
	}
	return fmt.Sprintf("  synchronized output  %s (%s)", onOff(syncState.on), how)
}
