package bashrun

import (
	"context"
	"os"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/sandbox"
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

// TestSandboxedChildDoesNotInheritCredentials runs through the platform
// sandbox: credential-looking variables are dropped, the markers and ordinary
// variables stay, and an unsandboxed run is unchanged.
func TestSandboxedChildDoesNotInheritCredentials(t *testing.T) {
	root := t.TempDir()
	shell, unset := "", "unset"
	command := `printf 'plain=%s key=%s marker=%s' "${KBRAIN_TEST_PLAIN-unset}" "${KBRAIN_TEST_API_KEY-unset}" "${K_BRAIN-unset}"`
	var policy *sandbox.Policy
	if runtime.GOOS == "windows" {
		t.Setenv("LIVEAGENT_HOME", t.TempDir())
		t.Setenv("K_BRAIN_HOME", "")
		// cmd.exe leaves an unset %VAR% as written.
		shell, unset = "cmd", "%KBRAIN_TEST_API_KEY%"
		command = `echo plain=%KBRAIN_TEST_PLAIN% key=%KBRAIN_TEST_API_KEY% marker=%K_BRAIN%`
		// Without masks: the tests never label the real credential directories.
		policy = &sandbox.Policy{Mode: "workspace", Backend: "auto", Root: root, Network: true, Protected: sandbox.ProtectedPaths(root)}
	} else {
		var err error
		if policy, err = sandbox.ForRun(nil, sandbox.RunRequest{Root: root, Network: true}); err != nil {
			t.Skipf("platform sandbox unavailable: %v", err)
		}
	}
	t.Setenv("KBRAIN_TEST_PLAIN", "plain")
	t.Setenv("KBRAIN_TEST_API_KEY", "key-value")
	res := Run(context.Background(), Options{Command: command, Shell: shell, Dir: root, Timeout: time.Minute, Sandbox: policy})
	if got := strings.TrimSpace(res.Output); got != "plain=plain key="+unset+" marker=1" {
		t.Fatalf("sandboxed child output = %q (%s)", res.Output, res.Exit)
	}
	res = Run(context.Background(), Options{Command: command, Shell: shell, Dir: root, Timeout: time.Minute})
	if got := strings.TrimSpace(res.Output); got != "plain=plain key=key-value marker=1" {
		t.Fatalf("unsandboxed child output = %q (%s)", res.Output, res.Exit)
	}
}
