package main

import (
	"strings"
	"testing"
)

func TestSandboxCLIArguments(t *testing.T) {
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	for _, args := range [][]string{nil, {"bogus"}, {"setup", "uninstall"}, {"--status", "setup"}} {
		if err := sandboxCLI(args); err == nil {
			t.Errorf("sandboxCLI(%q) accepted bad arguments", args)
		} else if !strings.Contains(err.Error(), "usage: kn sandbox") {
			t.Errorf("sandboxCLI(%q) error lacks the usage: %v", args, err)
		}
	}
	for _, args := range [][]string{{"status"}, {"--status", "--json"}, {"--help"}} {
		if err := sandboxCLI(args); err != nil {
			t.Errorf("sandboxCLI(%q) = %v", args, err)
		}
	}
}
