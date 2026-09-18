package computer

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

var ErrUnsupportedPlatform = errors.New("computer-use driver is unavailable on this platform")

func Available() bool {
	return runtime.GOOS == "darwin" || runtime.GOOS == "linux" || runtime.GOOS == "windows"
}

func osascript(script string) (string, error) {
	if runtime.GOOS != "darwin" {
		return "", ErrUnsupportedPlatform
	}
	cmd := exec.CommandContext(context.Background(), "osascript", "-e", script)
	out, err := cmd.CombinedOutput()
	s := strings.TrimRight(string(out), "\n")
	if err != nil {
		if strings.Contains(s, "User canceled") {
			return s, nil
		}
		return s, fmt.Errorf("osascript: %w: %s", err, s)
	}
	return s, nil
}

func quote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

func Tell(app string, commands ...string) (string, error) {
	var b strings.Builder
	b.WriteString("tell application " + quote(app) + "\n")
	for _, c := range commands {
		b.WriteString(c + "\n")
	}
	b.WriteString("end tell")
	return osascript(b.String())
}
