// Package testhome keeps a test binary away from the developer's data directory.
package testhome

import (
	"os"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/datapath"
)

var realHome string

// RealHome is the home directory from before Run, for tests that look for tools installed there.
func RealHome() string { return realHome }

// Run points the home directory at an empty temporary one and clears the data directory
// overrides, then runs the tests. Without it, agents built in tests read the real data
// directory, and the saved memory there ends up in the prompts the tests assert on. Call it from
// TestMain.
func Run(m *testing.M) int {
	realHome, _ = os.UserHomeDir()
	dir, err := os.MkdirTemp("", "kbrain-test-home-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	for _, name := range []string{"HOME", "USERPROFILE"} {
		if err := os.Setenv(name, dir); err != nil {
			panic(err)
		}
	}
	for _, name := range []string{datapath.LiveAgentHomeEnv, datapath.LegacyHomeEnv} {
		if err := os.Unsetenv(name); err != nil {
			panic(err)
		}
	}
	return m.Run()
}
