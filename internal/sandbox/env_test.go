package sandbox

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestFilterEnvDropsCredentialsUnlessKept(t *testing.T) {
	env := []string{
		"PATH=/usr/bin", "HOME=/home/u", "LANG=C.UTF-8", "SSH_AUTH_SOCK=/tmp/agent",
		"GITHUB_TOKEN=t", "aws_secret_access_key=s", "AWS_ACCESS_KEY_ID=k", "PGPASSWORD=p",
		"GIT_CREDENTIAL_HELPER=h", "AZURE_DEVOPS_PAT=x", "LD_PRELOAD=/x.so", "DYLD_INSERT_LIBRARIES=/x.dylib",
		"NPM_TOKEN=n", "CI_JOB_TOKEN=c",
	}
	p := &Policy{Mode: "workspace", EnvKeep: []string{"npm_token", "CI_*"}}
	got := p.FilterEnv(env)
	want := []string{"PATH=/usr/bin", "HOME=/home/u", "LANG=C.UTF-8", "SSH_AUTH_SOCK=/tmp/agent", "NPM_TOKEN=n", "CI_JOB_TOKEN=c"}
	if !slices.Equal(got, want) {
		t.Fatalf("FilterEnv = %q, want %q", got, want)
	}
	if off := (&Policy{Mode: "off"}).FilterEnv(env); !slices.Equal(off, env) {
		t.Fatalf("disabled policy filtered: %q", off)
	}
	var none *Policy
	if got := none.FilterEnv(env); !slices.Equal(got, env) {
		t.Fatalf("nil policy filtered: %q", got)
	}
}

func TestForRunCarriesEnvKeep(t *testing.T) {
	withProbe(t, Capability{Supported: true, NetworkControl: true})
	root := t.TempDir()
	p, err := ForRun(nil, RunRequest{Root: root, Network: true, EnvKeep: []string{"NPM_TOKEN"}})
	if err != nil || !slices.Equal(p.EnvKeep, []string{"NPM_TOKEN"}) {
		t.Fatalf("run policy %+v %v", p, err)
	}
	base := &Policy{Mode: "workspace", Root: root, Network: true, EnvKeep: []string{"HF_TOKEN"}}
	p, err = ForRun(base, RunRequest{Root: root, Network: true, EnvKeep: []string{"NPM_TOKEN"}})
	if err != nil || !slices.Equal(p.EnvKeep, []string{"HF_TOKEN", "NPM_TOKEN"}) {
		t.Fatalf("tightened policy %+v %v", p, err)
	}
	if !slices.Equal(base.EnvKeep, []string{"HF_TOKEN"}) {
		t.Fatal("ForRun modified the configured policy")
	}
}

// TestLiveSandboxFiltersEnvironment checks the variables reach the confined
// command filtered, whether the caller set cmd.Env or left it to inherit.
func TestLiveSandboxFiltersEnvironment(t *testing.T) {
	liveSandbox(t)
	root := t.TempDir()
	p := &Policy{Mode: "workspace", Backend: "auto", Root: root, Network: true, Masked: MaskedDirs(), Protected: ProtectedPaths(root), EnvKeep: []string{"KBRAIN_TEST_KEPT_TOKEN"}}
	script := `printf 'plain=%s secret=%s kept=%s\n' "${KBRAIN_TEST_PLAIN-unset}" "${KBRAIN_TEST_API_KEY-unset}" "${KBRAIN_TEST_KEPT_TOKEN-unset}"`
	want := "plain=p secret=unset kept=k"

	explicit := exec.Command("/bin/sh", "-c", script)
	explicit.Dir = root
	explicit.Env = []string{"PATH=" + os.Getenv("PATH"), "KBRAIN_TEST_PLAIN=p", "KBRAIN_TEST_API_KEY=s", "KBRAIN_TEST_KEPT_TOKEN=k"}
	if got := runWrapped(t, p, explicit); got != want {
		t.Fatalf("explicit env: %q, want %q", got, want)
	}

	t.Setenv("KBRAIN_TEST_PLAIN", "p")
	t.Setenv("KBRAIN_TEST_API_KEY", "s")
	t.Setenv("KBRAIN_TEST_KEPT_TOKEN", "k")
	inherited := exec.Command("/bin/sh", "-c", script)
	inherited.Dir = root
	if got := runWrapped(t, p, inherited); got != want {
		t.Fatalf("inherited env: %q, want %q", got, want)
	}
}

func runWrapped(t *testing.T, p *Policy, cmd *exec.Cmd) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell script")
	}
	wrapped, err := p.Wrap(context.Background(), cmd)
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}
	out, err := wrapped.CombinedOutput()
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	return strings.TrimSpace(string(out))
}
