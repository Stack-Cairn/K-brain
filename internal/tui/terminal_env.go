package tui

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

func enableClickWheelMouse(w io.Writer) {
	fmt.Fprint(w, "\x1b[?1006h\x1b[?1002h")
}

func disableClickWheelMouse(w io.Writer) {
	fmt.Fprint(w, "\x1b[?1003l\x1b[?1002l\x1b[?1000l\x1b[?1006l")
}

func enableKeyboardEnhancement(w io.Writer) {
	fmt.Fprint(w, tmuxPassthrough("\x1b[>1u"))
	if inTmuxEnv() {
		fmt.Fprint(w, "\x1b[>4;1m")
	}
}

func disableKeyboardEnhancement(w io.Writer) {
	fmt.Fprint(w, tmuxPassthrough("\x1b[<u"))
	if inTmuxEnv() {
		fmt.Fprint(w, "\x1b[>4;0m")
	}
}

func tmuxEnableExtendedKeys() {
	if tmuxExtendedKeysReady() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = exec.CommandContext(ctx, "tmux", "set", "-s", "extended-keys", "on").Run()
}

func tmuxPassthrough(seq string) string {
	if !inTmuxEnv() {
		return seq
	}
	return "\x1bPtmux;" + strings.ReplaceAll(seq, "\x1b", "\x1b\x1b") + "\x1b\\"
}

func tmuxExtendedKeysReady() bool {
	if !inTmuxEnv() {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "tmux", "display-message", "-p", "#{extended-keys}").Output()
	if err != nil {
		return true
	}
	v := strings.TrimSpace(string(out))
	return v == "on" || v == "always"
}

type bgResult struct {
	light, valid bool
	r, g, b      int
	hasRGB       bool
}

func inTmuxEnv() bool {
	return os.Getenv("TMUX") != "" ||
		strings.HasPrefix(os.Getenv("TERM"), "screen") ||
		strings.HasPrefix(os.Getenv("TERM"), "tmux")
}

func procHasAncestor(pid int, want string) bool {
	for i := 0; i < 64 && pid > 1; i++ {
		comm, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
		if err != nil {
			return false
		}
		if strings.TrimSpace(string(comm)) == want {
			return true
		}
		stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			return false
		}

		idx := bytes.LastIndexByte(stat, ')')
		if idx < 0 {
			return false
		}
		fields := strings.Fields(string(stat[idx+1:]))
		if len(fields) < 2 {
			return false
		}
		ppid, err := strconv.Atoi(fields[1])
		if err != nil || ppid == pid || ppid <= 1 {
			return false
		}
		pid = ppid
	}
	return false
}

var moshDetect = detectMosh

var tmuxExtKeysCheck = tmuxExtendedKeysReady

func inMoshEnv() bool { return moshDetect() }

func detectMosh() bool {
	if procHasAncestor(os.Getpid(), "mosh-server") {
		return true
	}
	if inTmuxEnv() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "tmux", "display-message", "-p", "#{client_pid}").Output()
		if err == nil {
			if cpid, err := strconv.Atoi(strings.TrimSpace(string(out))); err == nil && cpid > 1 {
				return procHasAncestor(cpid, "mosh-server")
			}
		}
	}
	return false
}

func detectColorScheme() string {
	setScheme := func(light bool) {
		SetLightTheme(light)
		lipgloss.SetHasDarkBackground(!light)
	}
	themeEnv := os.Getenv("K_BRAIN_THEME")
	themeSource := "K_BRAIN_THEME"
	switch strings.ToLower(themeEnv) {
	case "light":
		setScheme(true)
		return themeSource
	case "dark":
		setScheme(false)
		return themeSource
	}

	if tuiRunning {
		if bgCache.valid {
			setScheme(bgCache.light)
			return "terminal query (cached from startup)"
		}
		light, ok, how := fallbackScheme(inTmuxEnv(), os.Getenv("COLORFGBG"))
		if ok {
			setScheme(light)
		} else {
			setUnknownScheme()
		}
		return how
	}

	inTmux := inTmuxEnv()
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err == nil {
		if r := queryTerminalBackground(tty, inTmux); r.valid {
			_ = tty.Close()
			setScheme(r.light)
			bgCache = r
			if inTmux {
				return "terminal query (inside tmux)"
			}
			return "terminal query"
		}

		if !inTmux {
			type result struct{ light bool }
			done := make(chan result, 1)
			go func() {
				o := termenv.NewOutput(tty)
				done <- result{light: !o.HasDarkBackground()}
			}()
			select {
			case r := <-done:
				_ = tty.Close()
				setScheme(r.light)
				bgCache = bgResult{light: r.light, valid: true}
				return "terminal query"
			case <-time.After(300 * time.Millisecond):
			}
		}
		_ = tty.Close()
	}
	light, ok, how := fallbackScheme(inTmux, os.Getenv("COLORFGBG"))
	if ok {
		setScheme(light)
	} else {
		setUnknownScheme()
	}
	return how
}

// setUnknownScheme uses the neutral palette when the background could not be
// detected. It also pins lipgloss to its dark default: left unset, the first
// adaptive color or markdown render asks the terminal itself (an OSC 11 query
// with a multi-second timeout per byte) on the UI goroutine, and while
// bubbletea is reading stdin the reply never arrives, so the screen freezes
// for seconds the first time a reply is drawn.
func setUnknownScheme() {
	SetUnknownTheme()
	lipgloss.SetHasDarkBackground(true)
}

func fallbackScheme(inTmux bool, colorfgbg string) (light, ok bool, how string) {
	if i := strings.LastIndex(colorfgbg, ";"); i >= 0 {
		var bg int
		if _, err := fmt.Sscanf(colorfgbg[i+1:], "%d", &bg); err == nil {

			return bg == 7 || bg >= 8, true, "COLORFGBG (query failed)"
		}
	}
	if inTmux {
		return false, false, "undetermined (no OSC 11 reply through tmux — outer terminal doesn't answer it (e.g. mosh), or tmux <3.4 without `allow-passthrough on`) — neutral default"
	}
	return false, false, "undetermined (query timed out) — neutral default"
}
