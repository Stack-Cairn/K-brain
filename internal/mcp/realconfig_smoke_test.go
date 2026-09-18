package mcp

import (
	"os"
	"testing"
)

func TestRealCodexConfigSmoke(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	path := home + "/.codex/config.toml"
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skip("no codex config:", err)
	}
	cfgs, err := ParseCodex(data)
	if err != nil {
		t.Fatal(err)
	}
	for name := range cfgs {
		if name == "incident_io.tools.ask_telemetry" {
			t.Errorf("tool approval table leaked as server %q", name)
		}
	}
	if _, ok := cfgs["incident_io"]; ok {
		t.Logf("incident_io: url=%q headers=%v note=%q", cfgs["incident_io"].URL, cfgs["incident_io"].Headers, cfgs["incident_io"].Note)
	}
}
