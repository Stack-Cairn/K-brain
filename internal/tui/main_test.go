package tui

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "k-brain-test-home")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	os.Setenv("K_BRAIN_HOME", dir)

	SetLightTheme(false)

	moshDetect = func() bool { return false }

	tmuxExtKeysCheck = func() bool { return true }
	os.Exit(m.Run())
}
