package tui

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

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
	term   io.Writer
}

func systemClipboard() clipboard {
	return clipboard{env: currentClipboardEnv(), native: writeNativeClipboard, term: os.Stdout}
}

// copyText tries the local clipboard first, except over SSH where the local
// tools belong to the remote host, and falls back to OSC 52 so copies still
// reach the user's own terminal through SSH, tmux and containers.
func (c clipboard) copyText(text string) (copyRoute, error) {
	if text == "" {
		return 0, errors.New("nothing to copy")
	}
	var nativeErr error
	if !c.env.remote && c.native != nil {
		if nativeErr = c.native(text); nativeErr == nil {
			return copyNative, nil
		}
	}
	if c.env.noOSC52 || c.term == nil {
		if nativeErr == nil {
			nativeErr = errors.New("no clipboard route available")
		}
		return 0, fmt.Errorf("clipboard unavailable: %v", nativeErr)
	}
	if len(text) > osc52MaxBytes {
		return 0, errOSC52TooLarge
	}
	if _, err := io.WriteString(c.term, osc52Sequence(text, c.env.tmux)); err != nil {
		return 0, fmt.Errorf("clipboard unavailable: %v", err)
	}
	return copyOSC52, nil
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
