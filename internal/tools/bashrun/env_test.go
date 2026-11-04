package bashrun

import (
	"context"
	"os"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestScrubEnvDropsSecrets(t *testing.T) {
	env := []string{"PATH=/bin", "K_BRAIN_BACKEND_TOKEN=secret", "K_BRAIN_COMPUTER_TOKEN=other", "K_BRAIN_HOME=/data"}
	got := ScrubEnv(env)
	if !slices.Equal(got, []string{"PATH=/bin", "K_BRAIN_HOME=/data"}) {
		t.Fatalf("ScrubEnv = %v", got)
	}
}

func TestChildDoesNotInheritBackendToken(t *testing.T) {
	t.Setenv("K_BRAIN_BACKEND_TOKEN", "bearer-secret-value")
	command := `printf 'token=%s' "$K_BRAIN_BACKEND_TOKEN"`
	if runtime.GOOS == "windows" {
		command = `Write-Output "token=$env:K_BRAIN_BACKEND_TOKEN"`
	}
	res := Run(context.Background(), Options{Command: command, Timeout: 5 * time.Second})
	if strings.Contains(res.Output, "bearer-secret-value") || !strings.Contains(res.Output, "token=") {
		t.Fatalf("child output = %q", res.Output)
	}
}

func TestUnsetSecretEnv(t *testing.T) {
	t.Setenv("K_BRAIN_BACKEND_TOKEN", "bearer-secret-value")
	t.Setenv("K_BRAIN_COMPUTER_TOKEN", "helper-secret-value")
	UnsetSecretEnv()
	for _, name := range SecretEnvVars {
		if _, ok := os.LookupEnv(name); ok {
			t.Fatalf("%s still set", name)
		}
	}
}
