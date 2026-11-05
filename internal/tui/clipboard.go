package tui

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/process"
	tea "github.com/charmbracelet/bubbletea"
)

// copyRoute names how copied text left the TUI.
type copyRoute int

const (
	// copyNative means a local clipboard tool accepted the text.
	copyNative copyRoute = iota + 1
	// copyOSC52 means the text was sent to the terminal as an OSC 52
	// sequence. The terminal never confirms it, so it is best effort.
	copyOSC52
)

// osc52MaxBytes caps text sent through OSC 52. Many terminals silently drop
// larger payloads, so refusing is more honest than claiming a copy.
const osc52MaxBytes = 100_000

var errOSC52TooLarge = errors.New("text too large for the terminal clipboard (OSC 52)")

// clipboardEnv is the environment the clipboard routes are chosen from.
type clipboardEnv struct {
	remote  bool // SSH session: a native tool would fill the remote host's clipboard
	tmux    bool
	noOSC52 bool
}

func currentClipboardEnv() clipboardEnv {
	return clipboardEnv{
		remote:  os.Getenv("SSH_TTY") != "" || os.Getenv("SSH_CONNECTION") != "",
		tmux:    inTmuxEnv(),
		noOSC52: os.Getenv("K_BRAIN_CLIPBOARD_NO_OSC52") == "1",
	}
}

// clipboard writes text to the user's clipboard. Tests replace these hooks.
type clipboard struct {
	env    clipboardEnv
	native func(string) error
	// readback reads the native clipboard back so a copy can be confirmed;
	// nil skips the check.
	readback func() (string, error)
	term     io.Writer
	// backup keeps the copied text on disk and returns the file's path; nil
	// skips it.
	backup func(string) (string, error)
}

func systemClipboard() clipboard {
	return clipboard{env: currentClipboardEnv(), native: writeNativeClipboard, readback: readNativeClipboard, term: os.Stdout, backup: writeCopyBackup}
}

// copyReport is what became of one copy.
type copyReport struct {
	route copyRoute
	// verified is set when the native clipboard was read back and held the
	// text. OSC 52 is never verified: terminals do not answer it.
	verified bool
	// backup is the path the text was also saved to, or "".
	backup string
	err    error
}

// copyText tries the local clipboard first, except over SSH where the local
// tools belong to the remote host, and falls back to OSC 52 so copies still
// reach the user's own terminal through SSH, tmux and containers.
func (c clipboard) copyText(text string) (copyRoute, error) {
	r := c.deliver(text)
	return r.route, r.err
}

// deliver copies text and reports how far it is known to have got: a native
// copy is read back when possible, and a native tool that claims success
// while the clipboard holds something else counts as failed, so OSC 52 is
// tried next. The text is saved to the backup file first, so a copy that
// went nowhere can still be recovered.
func (c clipboard) deliver(text string) copyReport {
	var r copyReport
	if text == "" {
		r.err = errors.New("nothing to copy")
		return r
	}
	if c.backup != nil {
		if path, err := c.backup(text); err == nil {
			r.backup = path
		}
	}
	var nativeErr error
	if !c.env.remote && c.native != nil {
		if nativeErr = c.native(text); nativeErr == nil {
			if c.readback == nil {
				r.route = copyNative
				return r
			}
			got, err := c.readback()
			switch {
			case err != nil:
				// No way to read it back: the tool's word is all there is.
				r.route = copyNative
				return r
			case sameClipboardText(got, text):
				r.route, r.verified = copyNative, true
				return r
			}
			nativeErr = errors.New("the clipboard did not take the text")
		}
	}
	if c.env.noOSC52 || c.term == nil {
		if nativeErr == nil {
			nativeErr = errors.New("no clipboard route available")
		}
		r.err = fmt.Errorf("clipboard unavailable: %v", nativeErr)
		return r
	}
	if len(text) > osc52MaxBytes {
		r.err = errOSC52TooLarge
		return r
	}
	if _, err := io.WriteString(c.term, osc52Sequence(text, c.env.tmux)); err != nil {
		r.err = fmt.Errorf("clipboard unavailable: %v", err)
		return r
	}
	r.route = copyOSC52
	return r
}

// sameClipboardText compares a read-back clipboard with the copied text,
// forgiving the line-ending and trailing-newline changes clipboard tools make.
func sameClipboardText(got, want string) bool {
	norm := func(s string) string { return strings.TrimRight(strings.ReplaceAll(s, "\r\n", "\n"), "\n") }
	return norm(got) == norm(want)
}

// copyBackupName is the file in the K-brain data directory that holds the
// last copied text.
const copyBackupName = "last-copy.txt"

// writeCopyBackup saves text to last-copy.txt in the data directory,
// readable only by the user.
func writeCopyBackup(text string) (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, copyBackupName)
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		return "", err
	}
	_ = os.Chmod(path, 0o600)
	return path, nil
}

// readNativeClipboard reads the local clipboard with the platform's paste
// tool.
func readNativeClipboard() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var commands [][]string
	switch runtime.GOOS {
	case "windows":
		commands = [][]string{{"powershell.exe", "-NoProfile", "-NonInteractive", "-Command", "[Console]::OutputEncoding = [System.Text.UTF8Encoding]::new(); Get-Clipboard -Raw"}}
	case "darwin":
		commands = [][]string{{"pbpaste"}}
	default:
		commands = [][]string{{"wl-paste", "--no-newline"}, {"xclip", "-selection", "clipboard", "-o"}, {"xsel", "--clipboard", "--output"}}
	}
	last := errors.New("no clipboard reader")
	for _, args := range commands {
		path, err := exec.LookPath(args[0])
		if err != nil {
			last = err
			continue
		}
		cmd := exec.CommandContext(ctx, path, args[1:]...)
		process.Configure(cmd, false)
		cmd.WaitDelay = time.Second
		out, err := cmd.Output()
		if err == nil {
			return string(out), nil
		}
		last = err
	}
	return "", last
}

// osc52Sequence encodes text as an OSC 52 "set clipboard" request, wrapped
// for tmux passthrough when running inside tmux.
func osc52Sequence(text string, tmux bool) string {
	seq := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(text)) + "\a"
	if tmux {
		seq = "\x1bPtmux;" + strings.ReplaceAll(seq, "\x1b", "\x1b\x1b") + "\x1b\\"
	}
	return seq
}

func writeNativeClipboard(text string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var commands [][]string
	switch runtime.GOOS {
	case "windows":
		commands = [][]string{{"powershell.exe", "-NoProfile", "-NonInteractive", "-Command", "[Console]::InputEncoding = [System.Text.UTF8Encoding]::new(); Set-Clipboard -Value ([Console]::In.ReadToEnd())"}}
	case "darwin":
		commands = [][]string{{"pbcopy"}}
	default:
		commands = [][]string{{"wl-copy"}, {"xclip", "-selection", "clipboard"}, {"xsel", "--clipboard", "--input"}}
	}
	var last error
	for _, args := range commands {
		path, err := exec.LookPath(args[0])
		if err != nil {
			last = err
			continue
		}
		cmd := exec.CommandContext(ctx, path, args[1:]...)
		process.Configure(cmd, false)
		cmd.WaitDelay = time.Second
		cmd.Stdin = strings.NewReader(text)
		if err = cmd.Run(); err == nil {
			return nil
		}
		last = err
	}
	return last
}

// clipboardFor returns the clipboard copies go through; tests swap it out.
var clipboardFor = systemClipboard

type noticeExpiredMsg uint64

// showNotice shows msg in the footer for a couple of seconds.
func (m *model) showNotice(msg string) tea.Cmd {
	m.transientNotice = msg
	m.noticeGeneration++
	generation := m.noticeGeneration
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return noticeExpiredMsg(generation) })
}
