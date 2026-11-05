//go:build linux

package sandbox

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

// KBRAIN_REQUIRE_LANDLOCK=1 turns a missing Landlock into a failure, so CI
// cannot pass by skipping the tests below.
func landlockOnly(t *testing.T) {
	t.Helper()
	abi := landlockABI()
	if abi < 1 {
		if os.Getenv("KBRAIN_REQUIRE_LANDLOCK") == "1" {
			t.Fatal("Landlock is unavailable but KBRAIN_REQUIRE_LANDLOCK=1")
		}
		t.Skip("Landlock is unavailable in this kernel (needs 5.13+ with landlock in the LSM list)")
	}
	t.Logf("Landlock ABI %d", abi)
}

func landlockPolicy(t *testing.T, root string, network bool) *Policy {
	t.Helper()
	return &Policy{Mode: "workspace", Backend: mechanismLandlock, Root: root, Network: network, Masked: MaskedDirs(), Protected: ProtectedPaths(root)}
}

func landlockRun(t *testing.T, p *Policy, name string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = p.Root
	wrapped, err := p.Wrap(context.Background(), cmd)
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}
	if wrapped.Path != selfExe {
		t.Fatalf("landlock backend did not use the launcher: %s %q", wrapped.Path, wrapped.Args)
	}
	wrapped.Env = os.Environ()
	out, err := wrapped.CombinedOutput()
	return string(out), err
}

func TestLiveProbeReport(t *testing.T) {
	c := probePlatform()
	t.Logf("probe: supported=%v mechanism=%s network_control=%v read_masking=%v path_protection=%v reason=%q", c.Supported, c.Mechanism, c.NetworkControl, c.ReadMasking, c.PathProtection, c.Reason)
	// CI sets the mechanism it arranged for, so a silent switch fails.
	if want := os.Getenv("KBRAIN_EXPECT_SANDBOX_MECHANISM"); want != "" && (c.Mechanism != want || !c.Supported) {
		t.Fatalf("probe chose %s (supported %v), want %s", c.Mechanism, c.Supported, want)
	}
	if c.Mechanism == mechanismLandlock && (c.ReadMasking || c.PathProtection) {
		t.Fatal("the Landlock fallback claims read masking or path protection")
	}
	if _, filtered := seccompArches[runtime.GOARCH]; filtered && landlockABI() >= 1 && !c.Supported {
		t.Fatalf("Landlock is available but the probe reports unsupported: %s", c.Reason)
	}
}

func TestLandlockConfinesWrites(t *testing.T) {
	landlockOnly(t)
	root := t.TempDir()
	outside := outsideTempDir(t)
	p := landlockPolicy(t, root, true)
	if out, err := landlockRun(t, p, "/bin/sh", "-c", `echo ok > inside.txt && mkdir -p a/b && mv inside.txt a/b/ && cat a/b/inside.txt && echo x > /dev/null`); err != nil || strings.TrimSpace(out) != "ok" {
		t.Fatalf("workspace write failed: %q %v", out, err)
	}
	if out, err := landlockRun(t, p, "/bin/sh", "-c", `t=$(mktemp) && echo tmp > "$t" && cat "$t" && rm "$t"`); err != nil || strings.TrimSpace(out) != "tmp" {
		t.Fatalf("temp write failed: %q %v", out, err)
	}
	leak := filepath.Join(outside, "leak.txt")
	out, err := landlockRun(t, p, "/bin/sh", "-c", `echo x > `+leak)
	if err == nil {
		t.Fatalf("write outside the workspace succeeded: %q", out)
	}
	if _, statErr := os.Stat(leak); statErr == nil {
		t.Fatal("file written outside the workspace")
	}
	if !strings.Contains(strings.ToLower(out), "permission denied") {
		t.Fatalf("outside write failed without a permission error: %q", out)
	}
	// Moving a file from the workspace out of it is a write outside too.
	if err := os.WriteFile(filepath.Join(root, "moved"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := landlockRun(t, p, "/bin/mv", filepath.Join(root, "moved"), filepath.Join(outside, "moved")); err == nil {
		t.Fatalf("rename out of the workspace succeeded: %q", out)
	}
	if _, statErr := os.Stat(filepath.Join(outside, "moved")); statErr == nil {
		t.Fatal("file renamed out of the workspace")
	}
}

func TestLandlockRefusesConfiguredReadOnlyInsideRoot(t *testing.T) {
	landlockOnly(t)
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := landlockPolicy(t, root, true)
	p.ReadOnly = []string{"docs"}
	if _, err := p.Wrap(context.Background(), exec.Command("/bin/true")); err == nil {
		t.Fatal("landlock wrapped a command it cannot keep docs read-only for")
	}
}

func TestLandlockBlocksNetworkWhenOffline(t *testing.T) {
	landlockOnly(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("no loopback listener: %v", err)
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
	if _, err := os.Stat("/bin/bash"); err != nil {
		t.Skip("bash unavailable")
	}
	root := t.TempDir()
	probe := `exec 3<>/dev/tcp/` + strings.Replace(listener.Addr().String(), ":", "/", 1) + ` && echo connected`
	if out, err := landlockRun(t, landlockPolicy(t, root, true), "/bin/bash", "-c", probe); err != nil || !strings.Contains(out, "connected") {
		t.Fatalf("networked run could not connect: %q %v", out, err)
	}
	if out, err := landlockRun(t, landlockPolicy(t, root, false), "/bin/bash", "-c", probe); err == nil || strings.Contains(out, "connected") {
		t.Fatalf("offline run connected: %q %v", out, err)
	}
}

// TestLandlockSyscallFilter re-runs the test binary inside the launcher and
// checks the filtered calls from there.
func TestLandlockSyscallFilter(t *testing.T) {
	landlockOnly(t)
	if _, ok := seccompArches[runtime.GOARCH]; !ok {
		t.Skipf("no seccomp filter for %s", runtime.GOARCH)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, network := range []bool{false, true} {
		p := landlockPolicy(t, t.TempDir(), network)
		cmd := exec.Command(self, "-test.run=^TestLandlockFilterHelper$", "-test.v")
		cmd.Dir = p.Root
		wrapped, err := p.Wrap(context.Background(), cmd)
		if err != nil {
			t.Fatal(err)
		}
		wrapped.Env = append(os.Environ(), "KBRAIN_LANDLOCK_HELPER=1", fmt.Sprintf("KBRAIN_LANDLOCK_HELPER_NETWORK=%v", network))
		out, err := wrapped.CombinedOutput()
		if err != nil || !strings.Contains(string(out), "helper ok") {
			t.Fatalf("network=%v helper failed: %v\n%s", network, err, out)
		}
		t.Logf("network=%v helper:\n%s", network, out)
	}
}

func TestLandlockFilterHelper(t *testing.T) {
	if os.Getenv("KBRAIN_LANDLOCK_HELPER") != "1" {
		t.Skip("runs only inside TestLandlockSyscallFilter")
	}
	network := os.Getenv("KBRAIN_LANDLOCK_HELPER_NETWORK") == "true"
	socket := func(domain, typ int) error {
		fd, err := unix.Socket(domain, typ, 0)
		if err == nil {
			_ = unix.Close(fd)
		}
		return err
	}
	if err := socket(unix.AF_UNIX, unix.SOCK_STREAM); err != nil {
		t.Fatalf("AF_UNIX socket: %v", err)
	}
	if fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0); err != nil {
		t.Fatalf("AF_UNIX socketpair: %v", err)
	} else {
		_ = unix.Close(fds[0])
		_ = unix.Close(fds[1])
	}
	if err := socket(unix.AF_VSOCK, unix.SOCK_STREAM); !errors.Is(err, unix.EPERM) {
		t.Fatalf("AF_VSOCK socket = %v, want EPERM", err)
	}
	inet := socket(unix.AF_INET, unix.SOCK_STREAM)
	netlink := socket(unix.AF_NETLINK, unix.SOCK_RAW)
	if network {
		if inet != nil {
			t.Fatalf("AF_INET socket with network = %v", inet)
		}
	} else {
		if !errors.Is(inet, unix.EPERM) || !errors.Is(netlink, unix.EPERM) {
			t.Fatalf("offline AF_INET = %v, AF_NETLINK = %v, want EPERM", inet, netlink)
		}
	}
	if _, _, errno := unix.Syscall(unix.SYS_IO_URING_SETUP, 1, 0, 0); errno != unix.EPERM {
		t.Fatalf("io_uring_setup = %v, want EPERM", errno)
	}
	// On a non-terminal TIOCSTI would fail with ENOTTY; EPERM shows the
	// filter answered first.
	devNull, err := unix.Open("/dev/null", unix.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(devNull)
	var c byte = 'x'
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(devNull), unix.TIOCSTI, uintptr(unsafe.Pointer(&c))); errno != unix.EPERM {
		t.Fatalf("TIOCSTI = %v, want EPERM", errno)
	}
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(devNull), unix.TCGETS, 0); errno == unix.EPERM {
		t.Fatal("TCGETS was filtered")
	}
	if landlockABI() >= 6 {
		if err := unix.Kill(os.Getppid(), 0); !errors.Is(err, unix.EPERM) {
			t.Fatalf("signal to the parent outside the sandbox = %v, want EPERM", err)
		}
	}
	if err := unix.Prctl(unix.PR_GET_NO_NEW_PRIVS, 0, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	fmt.Println("helper ok")
}

func TestLauncherFailsClosed(t *testing.T) {
	landlockOnly(t)
	cases := []struct {
		name string
		args []string
		exit int
		want string
	}{
		{"bad spec", []string{"{", "/bin/true", "true"}, launcherFailedExit, "the command was not run"},
		{"missing argv", []string{`{"write":[]}`}, launcherFailedExit, "the command was not run"},
		{"missing program", []string{`{"write":[]}`, "/nonexistent/program", "program"}, 127, "/nonexistent/program"},
	}
	for _, tc := range cases {
		cmd := exec.Command(selfExe)
		cmd.Args = append([]string{launcherArg0}, tc.args...)
		out, err := cmd.CombinedOutput()
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != tc.exit || !strings.HasPrefix(string(out), launcherMessagePrefix+":") || !strings.Contains(string(out), tc.want) {
			t.Fatalf("%s: exit %v, output %q; want exit %d mentioning %q", tc.name, err, out, tc.exit, tc.want)
		}
	}
}
