package sandbox

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
)

func withProbe(t *testing.T, c Capability) {
	t.Helper()
	oldFunc := probeFunc
	probeFunc = func() Capability { return c }
	probeOnce = sync.Once{}
	t.Cleanup(func() {
		probeFunc = oldFunc
		probeOnce = sync.Once{}
	})
}

func TestForRunFailsClosedWhenUnsupported(t *testing.T) {
	withProbe(t, Capability{Reason: "no mechanism"})
	if _, err := ForRun(nil, RunRequest{Root: t.TempDir(), Network: true}); err == nil || !strings.Contains(err.Error(), "no mechanism") {
		t.Fatalf("ForRun on an unsupported machine = %v, want the reason", err)
	}
}

func TestForRunRefusesOfflineWithoutNetworkControl(t *testing.T) {
	withProbe(t, Capability{Supported: true, Reason: "no netns"})
	root := t.TempDir()
	if _, err := ForRun(nil, RunRequest{Root: root, Network: false}); err == nil {
		t.Fatal("offline sandbox accepted without network control")
	}
	if _, err := ForRun(nil, RunRequest{Root: root, Network: true}); err != nil {
		t.Fatalf("networked sandbox refused: %v", err)
	}
}

func TestForRunOnlyTightensConfiguredPolicy(t *testing.T) {
	withProbe(t, Capability{Supported: true, NetworkControl: true})
	root := t.TempDir()
	// "/data/kbrain" has no drive on Windows, so it would resolve under the root.
	mask, err := filepath.Abs("/data/kbrain")
	if err != nil {
		t.Fatal(err)
	}
	base := &Policy{Mode: "strict", Backend: "auto", Root: root, Network: false, Writable: []string{"cache"}, ReadOnly: []string{"docs"}}
	p, err := ForRun(base, RunRequest{Root: root, Writable: []string{t.TempDir()}, Network: true, Masked: []string{mask}})
	if err != nil {
		t.Fatal(err)
	}
	if p.Mode != "strict" || p.Network {
		t.Fatalf("run widened the configured policy: %+v", p)
	}
	if !slices.Equal(p.Writable, []string{"cache"}) || !slices.Equal(p.ReadOnly, []string{"docs"}) {
		t.Fatalf("run changed configured grants: %+v", p)
	}
	if !slices.Contains(p.Masked, mask) || len(p.Protected) == 0 {
		t.Fatalf("run did not add masks and protected paths: %+v", p)
	}
	if base.Masked != nil || base.Protected != nil {
		t.Fatal("ForRun modified the configured policy")
	}
	offline, err := ForRun(&Policy{Mode: "workspace", Root: root, Network: true}, RunRequest{Root: root, Network: false})
	if err != nil || offline.Network {
		t.Fatalf("offline run kept network: %+v %v", offline, err)
	}
}

func TestForRunRefusesRootEnclosingMask(t *testing.T) {
	withProbe(t, Capability{Supported: true, NetworkControl: true})
	root := t.TempDir()
	secret := filepath.Join(root, "secret")
	if _, err := ForRun(nil, RunRequest{Root: root, Network: true, Masked: []string{secret}}); err == nil {
		t.Fatal("root enclosing a masked directory was accepted")
	}
	inside := filepath.Join(secret, "project")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ForRun(nil, RunRequest{Root: inside, Network: true, Masked: []string{secret}}); err != nil {
		t.Fatalf("root inside a masked directory refused: %v", err)
	}
	if _, err := ForRun(nil, RunRequest{Root: inside, Writable: []string{root}, Network: true, Masked: []string{secret}}); err == nil {
		t.Fatal("extra writable root enclosing a masked directory was accepted")
	}
}

func TestDarwinUserTempParent(t *testing.T) {
	for in, want := range map[string]string{
		"/var/folders/s0/abc/T":         "/var/folders/s0/abc",
		"/private/var/folders/s0/abc/T": "/private/var/folders/s0/abc",
		"/tmp":                          "",
		"/Users/me/T":                   "",
		"/var/folders/s0/T":             "",
	} {
		if got := darwinUserTempParent(in); got != want {
			t.Fatalf("darwinUserTempParent(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolveBwrapSkipsWorkspace(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "node_modules", ".bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "bwrap"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := resolveBwrap(bin, root); strings.HasPrefix(got, root) {
		t.Fatalf("resolveBwrap picked the workspace binary %q", got)
	}
}

func TestSeatbeltProfileOrdersMasksBeforeRootAndReadOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("seatbelt profiles hold macOS paths; Windows paths are escaped differently")
	}
	root, err := canonicalPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := &Policy{Mode: "workspace", Root: root, Masked: []string{"/m/secret"}, Protected: []string{".liveagent"}}
	profile, err := p.seatbeltProfile(root)
	if err != nil {
		t.Fatal(err)
	}
	mask := strings.Index(profile, `(deny file-read* file-write* (subpath "/m/secret"))`)
	reallow := strings.LastIndex(profile, `(allow file-read* file-write* (subpath "`+root+`"))`)
	protect := strings.Index(profile, `(deny file-write* (subpath "`+filepath.Join(root, ".liveagent")+`"))`)
	network := strings.Index(profile, "(deny network*)")
	if mask < 0 || reallow < mask || protect < reallow || network < protect {
		t.Fatalf("profile rules out of order:\n%s", profile)
	}
	p.Network = true
	if profile, _ = p.seatbeltProfile(root); strings.Contains(profile, "(deny network*)") {
		t.Fatal("networked profile denies network")
	}
}

func TestBwrapArgsMaskThenRebindRootThenProtect(t *testing.T) {
	mask, err := canonicalPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(mask, "project")
	if err := os.MkdirAll(filepath.Join(root, ".git", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := &Policy{Mode: "workspace", Root: root, Masked: []string{mask}, Protected: ProtectedPaths(root)}
	args, err := p.bwrapArgs(root, root)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	tmpfs := strings.Index(joined, "--tmpfs "+mask)
	rebind := strings.LastIndex(joined, "--bind "+root+" "+root)
	hooks := filepath.Join(root, ".git", "hooks")
	protect := strings.Index(joined, "--ro-bind "+hooks+" "+hooks)
	if !strings.Contains(joined, "--unshare-net") || tmpfs < 0 || rebind < tmpfs || protect < rebind {
		t.Fatalf("bwrap args out of order: %s", joined)
	}
	if strings.Contains(joined, filepath.Join(root, ".liveagent")) {
		t.Fatalf("missing protected path was bound: %s", joined)
	}
	p.Network = true
	if args, _ = p.bwrapArgs(root, root); slices.Contains(args, "--unshare-net") {
		t.Fatal("networked policy unshares the network")
	}
}

// The tests below run real commands under the platform sandbox.

func outsideTempDir(t *testing.T) string {
	t.Helper()
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Skip("no user cache directory")
	}
	dir, err := os.MkdirTemp(cache, "kbrain-sandbox-test-")
	if err != nil {
		t.Skipf("cannot create a directory outside the temporary roots: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func confinedRun(t *testing.T, p *Policy, script string) (string, error) {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.Dir = p.Root
	wrapped, err := p.Wrap(context.Background(), cmd)
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}
	wrapped.Env = os.Environ()
	out, err := wrapped.CombinedOutput()
	return string(out), err
}

func liveSandbox(t *testing.T) {
	t.Helper()
	probeOnce = sync.Once{}
	t.Cleanup(func() { probeOnce = sync.Once{} })
	if c := Probe(); !c.Supported || !c.NetworkControl {
		t.Skipf("platform sandbox unavailable: %s", c.Reason)
	}
}

func TestLiveSandboxConfinesWrites(t *testing.T) {
	liveSandbox(t)
	root := t.TempDir()
	// Temporary directories are writable by design; probe a directory under the
	// user cache instead.
	outside := outsideTempDir(t)
	masked := t.TempDir()
	if err := os.WriteFile(filepath.Join(masked, "key"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".git", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	p, err := ForRun(nil, RunRequest{Root: root, Network: true, Masked: []string{masked}})
	if err != nil {
		t.Fatal(err)
	}
	if out, err := confinedRun(t, p, `echo ok > inside.txt && echo x > /dev/null && cat inside.txt`); err != nil || strings.TrimSpace(out) != "ok" {
		t.Fatalf("workspace write failed: %q %v", out, err)
	}
	if out, err := confinedRun(t, p, `t=$(mktemp) && echo tmp > "$t" && cat "$t" && rm "$t"`); err != nil || strings.TrimSpace(out) != "tmp" {
		t.Fatalf("temp write failed: %q %v", out, err)
	}
	if out, err := confinedRun(t, p, `echo x > `+filepath.Join(outside, "leak.txt")); err == nil {
		t.Fatalf("write outside the workspace succeeded: %q", out)
	}
	if _, statErr := os.Stat(filepath.Join(outside, "leak.txt")); statErr == nil {
		t.Fatal("file written outside the workspace")
	}
	if out, err := confinedRun(t, p, `cat `+filepath.Join(masked, "key")); err == nil || strings.Contains(out, "secret") {
		t.Fatalf("masked file readable: %q %v", out, err)
	}
	if out, err := confinedRun(t, p, `echo evil > .git/hooks/pre-commit`); err == nil {
		t.Fatalf("protected hook written: %q", out)
	}
}

func TestLiveSandboxRootInsideMask(t *testing.T) {
	liveSandbox(t)
	masked := t.TempDir()
	root := filepath.Join(masked, "default-project")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(masked, "config.json"), []byte("apikey"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := ForRun(nil, RunRequest{Root: root, Network: true, Masked: []string{masked}})
	if err != nil {
		t.Fatal(err)
	}
	if out, err := confinedRun(t, p, `echo ok > f && cat f`); err != nil || strings.TrimSpace(out) != "ok" {
		t.Fatalf("root inside mask unusable: %q %v", out, err)
	}
	if out, err := confinedRun(t, p, `cat ../config.json`); err == nil || strings.Contains(out, "apikey") {
		t.Fatalf("mask around the root readable: %q %v", out, err)
	}
}

func TestLiveSandboxBlocksNetworkWhenOffline(t *testing.T) {
	liveSandbox(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("no loopback listener")
	}
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	root := t.TempDir()
	probe := `exec 3<>/dev/tcp/` + strings.Replace(listener.Addr().String(), ":", "/", 1) + ` && echo connected`
	shell := "/bin/bash"
	if _, err := os.Stat(shell); err != nil {
		t.Skip("bash unavailable")
	}
	run := func(network bool) (string, error) {
		p, err := ForRun(nil, RunRequest{Root: root, Network: network})
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(shell, "-c", probe)
		cmd.Dir = root
		wrapped, err := p.Wrap(context.Background(), cmd)
		if err != nil {
			t.Fatal(err)
		}
		out, err := wrapped.CombinedOutput()
		return string(out), err
	}
	if out, err := run(true); err != nil || !strings.Contains(out, "connected") {
		t.Fatalf("networked sandbox could not connect: %q %v", out, err)
	}
	if out, err := run(false); err == nil && strings.Contains(out, "connected") {
		t.Fatalf("offline sandbox connected: %q", out)
	}
}
