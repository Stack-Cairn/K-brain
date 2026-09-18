//go:build !windows

package tui

import (
	"fmt"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

func queryTerminalBackground(tty *os.File, inTmux bool) bgResult {
	start := time.Now()
	fd := int(tty.Fd())
	if !isForegroundFd(fd) {
		config.LogEvent("theme.query", "skipped: not the foreground process group on the tty")
		return bgResult{}
	}
	query := bgQuery(inTmux)

	old, err := unix.IoctlGetTermios(fd, ioctlReadTermios)
	if err != nil {
		return bgResult{}
	}
	raw := *old
	raw.Lflag &^= unix.ECHO | unix.ICANON

	raw.Cc[unix.VMIN] = 0
	raw.Cc[unix.VTIME] = 1
	if err := unix.IoctlSetTermios(fd, ioctlWriteTermios, &raw); err != nil {
		return bgResult{}
	}
	defer unix.IoctlSetTermios(fd, ioctlWriteTermios, old)

	if _, err := tty.WriteString(query); err != nil {
		return bgResult{}
	}

	deadline := time.Now().Add(time.Second)
	var buf []byte
	chunk := make([]byte, 256)
	theme997 := byte(0)
	for time.Now().Before(deadline) {
		n, _ := tty.Read(chunk)
		if n == 0 {
			continue
		}
		buf = append(buf, chunk[:n]...)
		s := string(buf)
		if _, after, ok := strings.Cut(s, "\x1b]11;"); ok {

			rest := after
			end := strings.Index(rest, "\x07")
			if j := strings.Index(rest, "\x1b\\"); j >= 0 && (end < 0 || j < end) {
				end = j
			}
			if end < 0 {
				continue
			}
			r, g, b, ok := parseOSCBgRGB(rest[:end])
			if !ok {
				config.LogEvent("theme.query", fmt.Sprintf("reply unparseable after %s: %q", time.Since(start).Round(time.Millisecond), rest[:min(end, 60)]))
				return bgResult{}
			}
			config.LogEvent("theme.query", fmt.Sprintf("ok in %s: #%02x%02x%02x (inTmux=%v)", time.Since(start).Round(time.Millisecond), r, g, b, inTmux))
			return bgResult{light: rgbIsLight(r, g, b), valid: true, r: r, g: g, b: b, hasRGB: true}
		}
		if i := strings.Index(s, "\x1b[?997;"); i >= 0 && len(s) > i+7 {
			if c := s[i+7]; c == '1' || c == '2' {
				theme997 = c
			}
		}

		if theme997 != 0 && cprRE.MatchString(s) {
			light := theme997 == '2'
			config.LogEvent("theme.query", fmt.Sprintf("theme report in %s: light=%v (no rgb; inTmux=%v)", time.Since(start).Round(time.Millisecond), light, inTmux))
			return bgResult{light: light, valid: true}
		}
	}
	if theme997 != 0 {
		light := theme997 == '2'
		config.LogEvent("theme.query", fmt.Sprintf("theme report at deadline: light=%v (inTmux=%v)", light, inTmux))
		return bgResult{light: light, valid: true}
	}
	config.LogEvent("theme.query", fmt.Sprintf("no OSC 11 or 997 reply in %s (inTmux=%v); received %d bytes: %q",
		time.Since(start).Round(time.Millisecond), inTmux, len(buf), truncLine(fmt.Sprintf("%q", buf), 120)))
	return bgResult{}
}

func isForegroundFd(fd int) bool {
	pgrp, err := unix.IoctlGetInt(fd, unix.TIOCGPGRP)
	if err != nil {
		return false
	}
	return pgrp == unix.Getpgrp()
}
