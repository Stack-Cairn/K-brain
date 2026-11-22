//go:build !windows

package tui

import (
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

// querySyncOutputTTY asks the controlling terminal whether it implements
// mode 2026. answered is false when it could not ask or nothing came back in
// time.
func querySyncOutputTTY() (supported, answered bool) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return false, false
	}
	defer tty.Close()
	fd := int(tty.Fd())
	if !isForegroundFd(fd) {
		return false, false
	}
	old, err := unix.IoctlGetTermios(fd, ioctlReadTermios)
	if err != nil {
		return false, false
	}
	raw := *old
	raw.Lflag &^= unix.ECHO | unix.ICANON
	raw.Cc[unix.VMIN] = 0
	raw.Cc[unix.VTIME] = 1
	if err := unix.IoctlSetTermios(fd, ioctlWriteTermios, &raw); err != nil {
		return false, false
	}
	defer unix.IoctlSetTermios(fd, ioctlWriteTermios, old)

	start := time.Now()
	if _, err := tty.WriteString(syncQuery); err != nil {
		return false, false
	}
	deadline := start.Add(500 * time.Millisecond)
	var buf []byte
	chunk := make([]byte, 128)
	for time.Now().Before(deadline) {
		n, _ := tty.Read(chunk)
		if n == 0 {
			continue
		}
		buf = append(buf, chunk[:n]...)
		if supported, done := parseSyncReply(string(buf)); done {
			config.LogEvent("sync.query", fmt.Sprintf("supported=%v in %s", supported, time.Since(start).Round(time.Millisecond)))
			return supported, true
		}
	}
	config.LogEvent("sync.query", fmt.Sprintf("no answer in %s; received %q", time.Since(start).Round(time.Millisecond), truncLine(fmt.Sprintf("%q", buf), 80)))
	return false, false
}
