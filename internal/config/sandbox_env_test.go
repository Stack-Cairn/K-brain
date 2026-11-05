package config

import (
	"slices"
	"testing"
)

func TestSandboxEnvKeepReachesPolicy(t *testing.T) {
	var cfg Config
	if err := parseConfigJSONC([]byte(`{"sandbox": {"mode": "workspace", "envKeep": ["NPM_TOKEN", "CI_*"]}}`), &cfg); err != nil {
		t.Fatal(err)
	}
	p := cfg.Sandbox.Policy(t.TempDir())
	if p == nil || !slices.Equal(p.EnvKeep, []string{"NPM_TOKEN", "CI_*"}) {
		t.Fatalf("policy %+v", p)
	}
	// The policy owns its copy.
	cfg.Sandbox.EnvKeep[0] = "CHANGED"
	if p.EnvKeep[0] != "NPM_TOKEN" {
		t.Fatal("policy shares the configuration's slice")
	}
}
