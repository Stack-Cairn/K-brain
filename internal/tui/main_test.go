package tui

import (
	"errors"
	"io"
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

	// Never touch the developer's real clipboard or terminal from tests.
	clipboardFor = func() clipboard {
		return clipboard{native: func(string) error { return errors.New("no clipboard in tests") }, term: io.Discard}
	}
	os.Exit(m.Run())
}
