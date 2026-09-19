package tui

import (
	"strings"
	"testing"
)

func TestShellEscapeBinaryOutputGated(t *testing.T) {

	out := shellExec(testShellCommand(t, "printf '\\000\\001\\002\\003\\004\\005'", "$s = [Console]::OpenStandardOutput(); $s.Write([byte[]](0,1,2,3,4,5), 0, 6); $s.Flush()"))
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
