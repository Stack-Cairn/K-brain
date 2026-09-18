package tui

import (
	"strings"
	"testing"
)

func TestShellEscapeBinaryOutputGated(t *testing.T) {

	out := shellExec("printf '\\x00\\x01\\x02\\x03\\x04\\x05'")
	if !strings.Contains(out, "[binary output:") {
		t.Errorf("binary shell output was not gated: %q", out[:min(len(out), 80)])
	}
}

func TestShellEscapeTextOutputPasses(t *testing.T) {
	out := shellExec("echo hello")
	if !strings.Contains(out, "hello") {
		t.Errorf("text shell output was gated: %q", out)
	}
}
