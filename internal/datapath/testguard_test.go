package datapath

import (
	"os"
	"path/filepath"
	"testing"
)

// realLookingDir is a directory outside the temporary directory, the way a
// developer's home is. It sits next to the package sources and is removed
// afterwards.
func realLookingDir(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if InTempDir(wd) {
		t.Skip("the checkout itself is inside the temporary directory")
	}
	dir := filepath.Join(wd, "testdata-real-home")
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestUserDirUnderTestNeverUsesARealHome(t *testing.T) {
	home := realLookingDir(t)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv(LiveAgentHomeEnv, "")
	t.Setenv(LegacyHomeEnv, "")
	dir, err := UserDir()
	if err != nil {
		t.Fatal(err)
	}
	if !InTempDir(dir) {
		t.Fatalf("UserDir() = %q, outside the temporary directory", dir)
	}
	if _, err := os.Stat(filepath.Join(home, LiveAgentDirName)); !os.IsNotExist(err) {
		t.Fatalf("the real-looking home was written to: %v", err)
	}
}

func TestUserDirUnderTestIgnoresARealOverride(t *testing.T) {
	real := realLookingDir(t)
	for _, env := range []string{LiveAgentHomeEnv, LegacyHomeEnv} {
		t.Run(env, func(t *testing.T) {
			t.Setenv(LiveAgentHomeEnv, "")
			t.Setenv(LegacyHomeEnv, "")
			t.Setenv(env, real)
			dir, err := UserDir()
			if err != nil {
				t.Fatal(err)
			}
			if !InTempDir(dir) {
				t.Fatalf("UserDir() = %q with %s set to a real directory", dir, env)
			}
			if _, err := os.Stat(real); !os.IsNotExist(err) {
				t.Fatalf("the real override was created: %v", err)
			}
		})
	}
}

func TestUserDirUnderTestKeepsATemporaryHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv(LiveAgentHomeEnv, "")
	t.Setenv(LegacyHomeEnv, "")
	dir, err := UserDir()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, LiveAgentDirName); dir != want {
		t.Fatalf("UserDir() = %q, want %q", dir, want)
	}
	override := filepath.Join(t.TempDir(), "data")
	t.Setenv(LiveAgentHomeEnv, override)
	if dir, err := UserDir(); err != nil || dir != override {
		t.Fatalf("UserDir() = %q, %v; want the temporary override %q", dir, err, override)
	}
}

func TestInTempDir(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Clean(os.TempDir())
	for path, want := range map[string]bool{
		tmp:                          true,
		filepath.Join(tmp, "a", "b"): true,
		filepath.Dir(root):           false,
		root + "-sibling":            false,
	} {
		if got := InTempDir(path); got != want {
			t.Errorf("InTempDir(%q) = %v, want %v", path, got, want)
		}
	}
}
