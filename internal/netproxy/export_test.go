package netproxy

import (
	"os"
	"strings"
	"sync"
	"testing"
)

// resetForTest drops the published state so the next request re-reads the
// environment, and restores a fresh state when the test ends.
func resetForTest(t *testing.T) {
	t.Helper()
	reset := func() {
		initOnce = sync.Once{}
		current.Store(nil)
	}
	reset()
	t.Cleanup(reset)
}

// standaloneForTest unsets EnvVar for the test and resets the package so it
// starts in standalone mode. Set environment proxies before calling it.
func standaloneForTest(t *testing.T) {
	t.Helper()
	t.Setenv(EnvVar, "")
	if err := os.Unsetenv(EnvVar); err != nil {
		t.Fatal(err)
	}
	resetForTest(t)
}

// setEnvProxy sets the standard proxy variables for the test in both spellings.
// The environment parser prefers the lowercase names, so setting only the
// uppercase ones would let a developer's or runner's ambient http_proxy /
// no_proxy decide where test requests go.
func setEnvProxy(t *testing.T, httpProxy, httpsProxy, noProxy string) {
	t.Helper()
	for _, kv := range [][2]string{{"HTTP_PROXY", httpProxy}, {"HTTPS_PROXY", httpsProxy}, {"NO_PROXY", noProxy}} {
		t.Setenv(kv[0], kv[1])
		t.Setenv(strings.ToLower(kv[0]), kv[1])
	}
}
