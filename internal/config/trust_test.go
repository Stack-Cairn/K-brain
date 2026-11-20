package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTrustRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("K_BRAIN_HOME", home)
	first := filepath.Join(t.TempDir(), "first")
	second := filepath.Join(t.TempDir(), "second")
	if Trusted(first) {
		t.Fatal("should not be trusted initially")
	}
	if err := Trust(first); err != nil {
		t.Fatal(err)
	}
	if !Trusted(first) || Trusted(second) {
		t.Fatal("trust must be scoped to one project")
	}
	if !Trusted(filepath.Join(first, "nested")) {
		t.Fatal("a trusted project should cover its descendants")
	}
	data, err := os.ReadFile(filepath.Join(home, "trusted_folders.toml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "[folders.") || !strings.Contains(text, "trusted = true") || !strings.Contains(text, "decided_at = ") {
		t.Fatalf("unexpected trust TOML:\n%s", text)
	}
	if strings.Contains(text, "trusted.json") {
		t.Fatal("legacy JSON trust format must not be written")
	}
}

func TestTrustDoesNotMergeProjects(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	first, second := filepath.Join(t.TempDir(), "first"), filepath.Join(t.TempDir(), "second")
	if err := Trust(first); err != nil {
		t.Fatal(err)
	}
	if err := Trust(second); err != nil {
		t.Fatal(err)
	}
	if !Trusted(first) || !Trusted(second) {
		t.Fatal("both project records should remain trusted")
	}
}

func TestUntrustRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("K_BRAIN_HOME", home)
	project := filepath.Join(t.TempDir(), "project")
	other := filepath.Join(t.TempDir(), "other")
	if err := Trust(project); err != nil {
		t.Fatal(err)
	}
	if err := Trust(other); err != nil {
		t.Fatal(err)
	}
	if trusted, source := TrustDecision(project); !trusted || source != project {
		t.Fatalf("decision = %v %q", trusted, source)
	}
	if err := Untrust(project); err != nil {
		t.Fatal(err)
	}
	if Trusted(project) || !Trusted(other) {
		t.Fatal("revoke must only affect its own project")
	}
	data, err := os.ReadFile(filepath.Join(home, "trusted_folders.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), project) {
		t.Fatalf("a revoked project without a trusted ancestor should leave no record:\n%s", data)
	}
	if trusted, source := TrustDecision(project); trusted || source != "" {
		t.Fatalf("decision after revoke = %v %q", trusted, source)
	}
	if err := Trust(project); err != nil || !Trusted(project) {
		t.Fatalf("re-trust: %v", err)
	}
}

func TestUntrustUnderTrustedAncestorSticks(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	parent := t.TempDir()
	child := filepath.Join(parent, "child")
	if err := Trust(parent); err != nil {
		t.Fatal(err)
	}
	if trusted, source := TrustDecision(child); !trusted || source != parent {
		t.Fatalf("inherited decision = %v %q", trusted, source)
	}
	if err := Untrust(child); err != nil {
		t.Fatal(err)
	}
	if Trusted(child) || Trusted(filepath.Join(child, "nested")) {
		t.Fatal("revoking a folder under a trusted ancestor must stick")
	}
	if !Trusted(parent) {
		t.Fatal("the ancestor keeps its trust")
	}
	if trusted, source := TrustDecision(child); trusted || source != child {
		t.Fatalf("decision after revoke = %v %q", trusted, source)
	}
	if err := Trust(child); err != nil || !Trusted(child) {
		t.Fatalf("re-trust: %v", err)
	}
}

func TestUntrustWithoutRecordIsNoop(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	if err := Untrust(filepath.Join(t.TempDir(), "never")); err != nil {
		t.Fatal(err)
	}
}
