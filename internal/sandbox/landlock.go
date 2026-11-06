package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	mechanismBubblewrap = "bubblewrap"
	mechanismLandlock   = "landlock"

	// launcherArg0 is argv[0] of a re-exec of this binary as the Landlock
	// launcher (see launcher_linux.go).
	launcherArg0 = "k-brain-sandbox-launcher"
	// launcherMessagePrefix starts every error the launcher prints.
	launcherMessagePrefix = "k-brain sandbox"
	// launcherFailedExit is the exit status when the launcher could not
	// confine itself; the command did not run.
	launcherFailedExit = 125
	// selfExe names the running binary in the child, even after the file on
	// disk was replaced by an update.
	selfExe = "/proc/self/exe"
)

// launchSpec is what the launcher enforces.
type launchSpec struct {
	// Write lists the files and directories writes are allowed beneath.
	Write []string `json:"write"`
	// Network keeps IP sockets available; without it only AF_UNIX remains.
	Network bool `json:"network,omitempty"`
	// Protect lists the protected paths inside the write grants (symlinks
	// resolved). When set, the launcher keeps them unchanged with a
	// supervisor (see supervise_linux.go).
	Protect []string `json:"protect,omitempty"`
}

// landlockDevices are the device nodes and directories shells and tools write
// (terminals, /dev/null, POSIX shared memory). bubblewrap gets the same from
// --dev; Seatbelt allows the equivalent nodes.
var landlockDevices = []string{"/dev/null", "/dev/zero", "/dev/full", "/dev/random", "/dev/urandom", "/dev/tty", "/dev/ptmx", "/dev/pts", "/dev/shm"}

// wrapLinux picks the Linux mechanism. "auto" prefers bubblewrap and uses the
// Landlock launcher only when the probe found bubblewrap unusable, so a
// machine where bubblewrap works keeps read masking and protected paths.
func (p *Policy) wrapLinux(ctx context.Context, cmd *exec.Cmd, root string) (*exec.Cmd, error) {
	switch strings.ToLower(strings.TrimSpace(p.Backend)) {
	case mechanismLandlock:
		return p.wrapLandlock(ctx, cmd, root)
	case "", "auto":
		if Probe().Mechanism == mechanismLandlock {
			return p.wrapLandlock(ctx, cmd, root)
		}
	}
	return p.wrapBubblewrap(ctx, cmd, root)
}

// wrapLandlock runs cmd through the launcher, which allows writes only beneath
// the root, the extra writable paths, the temporary directories and the device
// nodes, and filters sockets with seccomp. Landlock cannot carve a read-only
// path out of a writable tree or hide a directory from reads, so:
//   - masked directories stay readable (the capability reports read_masking
//     false) but are never writable;
//   - the protected paths are kept by the launcher's supervisor where the
//     kernel supports it (see supervise_linux.go; reported as path_protection),
//     and otherwise stay writable inside the root;
//   - configured read-only paths inside a writable tree make Wrap fail, since
//     the configuration asks for something this mechanism cannot enforce.
func (p *Policy) wrapLandlock(ctx context.Context, cmd *exec.Cmd, root string) (*exec.Cmd, error) {
	if runtime.GOOS != "linux" {
		return nil, fmt.Errorf("sandbox backend landlock is only available on Linux")
	}
	if landlockABI() < 1 {
		return nil, errors.New("sandbox backend landlock requires Linux 5.13 or later with Landlock enabled")
	}
	if !isDir(root) {
		return nil, fmt.Errorf("sandbox root %q is not a directory", root)
	}
	spec, err := p.landlockSpec(root)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(spec)
	if err != nil {
		return nil, err
	}
	wrapped := exec.CommandContext(ctx, selfExe)
	wrapped.Args = append([]string{launcherArg0, string(encoded), cmd.Path}, cmd.Args...)
	wrapped.Env = cmd.Env
	wrapped.Dir = commandDir(cmd, root)
	return wrapped, nil
}

func (p *Policy) landlockSpec(root string) (launchSpec, error) {
	writable, err := resolveAll(root, p.Writable)
	if err != nil {
		return launchSpec{}, err
	}
	masked, err := resolveAll(root, p.Masked)
	if err != nil {
		return launchSpec{}, err
	}
	readOnly, err := resolveAll(root, p.ReadOnly)
	if err != nil {
		return launchSpec{}, err
	}
	grants := append([]string{root}, writable...)
	// A temporary directory that holds the home directory or a mask would
	// re-open it for writes; tempDirs drops such a candidate.
	grants = append(grants, tempDirs(append([]string{homeDir()}, masked...))...)
	for _, path := range readOnly {
		for _, grant := range grants {
			if encloses(grant, path) {
				return launchSpec{}, fmt.Errorf("sandbox backend landlock cannot keep %q read-only inside the writable %q; use bubblewrap or remove it from sandbox.readOnly", path, grant)
			}
		}
	}
	spec := launchSpec{Write: append(grants, landlockDevices...), Network: p.Network}
	if len(p.Protected) > 0 && landlockProtection() == nil {
		protected, err := p.protect(root, grants, true)
		if err != nil {
			return launchSpec{}, err
		}
		spec.Protect = protected.resolved
	}
	return spec, nil
}

// probeLinux reports bubblewrap when it can start, and otherwise the Landlock
// launcher with what that mechanism cannot enforce spelled out.
func probeLinux(ctx context.Context) Capability {
	c := Capability{Mechanism: mechanismBubblewrap, Platform: "linux"}
	var why string
	if bwrap := resolveBwrap(os.Getenv("PATH"), ""); bwrap == "" {
		why = "bubblewrap (bwrap) is not installed"
	} else {
		base := []string{"--die-with-parent", "--unshare-pid", "--ro-bind", "/", "/", "--dev", "/dev", "--proc", "/proc"}
		out, err := exec.CommandContext(ctx, bwrap, append(append(base, "--unshare-net"), "--", "/bin/true")...).CombinedOutput()
		if err == nil {
			c.Supported, c.NetworkControl, c.ReadMasking, c.PathProtection = true, true, true, true
			return c
		}
		if _, plainErr := exec.CommandContext(ctx, bwrap, append(base, "--", "/bin/true")...).CombinedOutput(); plainErr == nil {
			c.Supported, c.ReadMasking, c.PathProtection = true, true, true
			c.Reason = "bubblewrap cannot create a network namespace here: " + firstLine(out, err)
			return c
		}
		why = "bubblewrap cannot start: " + firstLine(out, err)
	}
	if hint := apparmorUsernsHint(); hint != "" {
		why += "; " + hint
	}
	landlock := probeLandlock(ctx)
	if !landlock.Supported {
		c.Reason = why + "; install it, e.g. `apt install bubblewrap`, or allow unprivileged user namespaces (" + landlock.Reason + ")"
		return c
	}
	landlock.Reason = why + "; " + landlock.Reason
	return landlock
}

// apparmorUsernsSysctl is replaced in tests.
var apparmorUsernsSysctl = "/proc/sys/kernel/apparmor_restrict_unprivileged_userns"

// apparmorUsernsHint explains the AppArmor restriction (Ubuntu 23.10 and
// later) that keeps bubblewrap from creating user namespaces, and how to lift
// it for bubblewrap alone or for the whole machine.
func apparmorUsernsHint() string {
	data, err := os.ReadFile(apparmorUsernsSysctl)
	if err != nil || strings.TrimSpace(string(data)) != "1" {
		return ""
	}
	bwrap := resolveBwrap(os.Getenv("PATH"), "")
	if bwrap == "" {
		bwrap = "/usr/bin/bwrap"
	}
	return "AppArmor restricts unprivileged user namespaces here (kernel.apparmor_restrict_unprivileged_userns=1), so bubblewrap needs a profile that allows them: " +
		"create /etc/apparmor.d/bwrap with `abi <abi/4.0>, include <tunables/global> profile bwrap " + bwrap + " flags=(unconfined) { userns, }` " +
		"and run `sudo apparmor_parser -r /etc/apparmor.d/bwrap`, or lift the restriction for every program with `sudo sysctl kernel.apparmor_restrict_unprivileged_userns=0`"
}

// probeLandlock checks the kernel's Landlock ABI and runs a trivial offline
// command through the launcher, so a binary or kernel that cannot apply the
// restrictions reports unsupported instead of failing every command.
func probeLandlock(ctx context.Context) Capability {
	c := Capability{Mechanism: mechanismLandlock, Platform: "linux"}
	abi := landlockABI()
	if abi < 1 {
		c.Reason = "Landlock is not available; it needs Linux 5.13 or later with landlock in the lsm= boot list"
		return c
	}
	if _, ok := seccompArches[runtime.GOARCH]; !ok {
		c.Reason = "the Landlock launcher has no seccomp filter for " + runtime.GOARCH
		return c
	}
	trueBin := "/bin/true"
	if path, err := exec.LookPath("true"); err == nil && filepath.IsAbs(path) {
		trueBin = path
	}
	spec, _ := json.Marshal(launchSpec{Write: []string{os.TempDir()}})
	probe := exec.CommandContext(ctx, selfExe)
	probe.Args = []string{launcherArg0, string(spec), trueBin, "true"}
	if out, err := probe.CombinedOutput(); err != nil {
		c.Reason = "the Landlock launcher cannot start: " + firstLine(out, err)
		return c
	}
	c.Supported, c.NetworkControl = true, true
	if err := landlockProtection(); err != nil {
		c.Reason = fmt.Sprintf("using Landlock (ABI %d) with seccomp: writes are fenced to the workspace and offline runs get no network, but credential directories stay readable and protected workspace paths (.git hooks and config, agent settings) stay writable (%v)", abi, err)
		return c
	}
	c.PathProtection = true
	c.Reason = fmt.Sprintf("using Landlock (ABI %d) with seccomp: writes are fenced to the workspace, a supervisor keeps the protected workspace paths (.git hooks and config, agent settings) unchanged and offline runs get no network, but credential directories stay readable", abi)
	return c
}
