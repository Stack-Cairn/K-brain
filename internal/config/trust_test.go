package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTrustRoundTrip(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	dir := "/home/abe/code/k-brain"
	if Trusted(dir) {
		t.Fatal("should not be trusted initially")
	}
	if err := Trust(dir); err != nil {
		t.Fatal(err)
	}
	if !Trusted(dir) {
		t.Fatal("should be trusted after Trust()")
	}

	if !Trusted(dir) {
		t.Fatal("trust should persist")
	}

	if Trusted("/other/path") {
		t.Fatal("unrelated path must not be trusted")
	}

	home, _ := Dir()
	if _, err := os.Stat(filepath.Join(home, "trusted.json")); err != nil {
		t.Fatalf("trusted.json missing: %v", err)
	}
}

func TestTrustMergesIntoExistingFile(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	if err := Trust("/first"); err != nil {
		t.Fatal(err)
	}
	if err := Trust("/second"); err != nil {
		t.Fatal(err)
	}
	if !Trusted("/first") || !Trusted("/second") {
		t.Fatal("both paths should stay trusted")
	}
}

func TestTrustRecoversFromNullPaths(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	home, err := Dir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "trusted.json"), []byte(`{"paths":null}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Trust("/rescued"); err != nil {
		t.Fatal(err)
	}
	if !Trusted("/rescued") {
		t.Fatal("trust over a null paths map should still record")
	}
}
