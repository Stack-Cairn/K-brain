package sandbox

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Capability reports whether this machine can confine a run's commands.
type Capability struct {
	Supported      bool   `json:"supported"`
	Mechanism      string `json:"mechanism"`
	Platform       string `json:"platform"`
	NetworkControl bool   `json:"network_control"`
	ReadMasking    bool   `json:"read_masking"`
	// PathProtection reports whether the protected workspace paths (git hooks
	// and config, agent settings) stay read-only inside a writable root.
	PathProtection bool   `json:"path_protection"`
	Reason         string `json:"reason,omitempty"`
}

var (
	probeOnce sync.Once
	probed    Capability
	// probeFunc is replaced in tests.
	probeFunc = probePlatform
)

// Probe checks the platform mechanism once per process by running a trivial
// confined command, so a binary that exists but cannot start (no user
// namespaces in a container) reports unsupported instead of failing later.
func Probe() Capability {
	probeOnce.Do(func() { probed = probeFunc() })
	return probed
}

func probePlatform() Capability {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	switch runtime.GOOS {
	case "darwin":
		c := Capability{Mechanism: "seatbelt", Platform: "macos"}
		if out, err := exec.CommandContext(ctx, sandboxExecPath, "-p", "(version 1)(allow default)", "/usr/bin/true").CombinedOutput(); err != nil {
			c.Reason = fmt.Sprintf("%s is unavailable: %s", sandboxExecPath, firstLine(out, err))
			return c
		}
		c.Supported, c.NetworkControl, c.ReadMasking, c.PathProtection = true, true, true, true
		return c
	case "linux":
		return probeLinux(ctx)
	case "windows":
		return probeWindows()
	default:
		return Capability{Mechanism: "none", Platform: runtime.GOOS, Reason: "no sandbox mechanism for " + runtime.GOOS}
	}
}

func firstLine(out []byte, err error) string {
	text := strings.TrimSpace(string(out))
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		text = text[:i]
	}
	if text == "" && err != nil {
		text = err.Error()
	}
	return text
}

// RunRequest is a run's request to confine its commands to the workspace.
type RunRequest struct {
	Root string
	// Writable lists the run's other writable workspace roots.
	Writable []string
	Network  bool
	// Masked and Protected extend the defaults (MaskedDirs, ProtectedPaths).
	Masked    []string
	Protected []string
}

// ForRun builds the policy for a run that asked for the workspace sandbox. A
// configured policy (base) is only ever tightened: its roots stay as they are,
// network stays off when either side turns it off, and the masks and protected
// paths are added. Without a configured policy the run's roots are the grants.
// The result fails closed: when the machine cannot confine commands an error is
// returned instead of a policy.
func ForRun(base *Policy, req RunRequest) (*Policy, error) {
	capability := Probe()
	if !capability.Supported {
		return nil, fmt.Errorf("sandbox is unavailable on this machine: %s", capability.Reason)
	}
	if !req.Network && !capability.NetworkControl {
		return nil, fmt.Errorf("offline sandbox is unavailable on this machine: %s", capability.Reason)
	}
	root, err := canonicalPath(req.Root)
	if err != nil {
		return nil, err
	}
	var p *Policy
	if base.Enabled() {
		p = base.ForRoot(root)
		p.Network = base.Network && req.Network
	} else {
		p = &Policy{Mode: "workspace", Backend: "auto", Root: root, Network: req.Network, Writable: append([]string(nil), req.Writable...)}
	}
	p.Masked = append(p.Masked, MaskedDirs()...)
	p.Masked = append(p.Masked, req.Masked...)
	p.Protected = append(p.Protected, ProtectedPaths(root)...)
	p.Protected = append(p.Protected, req.Protected...)
	if err := checkRoot(root, p.Masked); err != nil {
		return nil, err
	}
	for _, dir := range p.Writable {
		resolved, err := resolvePath(root, dir)
		if err != nil {
			return nil, err
		}
		if resolved, err = canonicalPath(resolved); err != nil {
			return nil, err
		}
		if err := checkRoot(resolved, p.Masked); err != nil {
			return nil, err
		}
	}
	return p, nil
}

// MaskedDirs are the credential directories hidden from sandboxed commands.
func MaskedDirs() []string {
	home := homeDir()
	if home == "" {
		return nil
	}
	var out []string
	for _, sub := range []string{".ssh", ".aws", ".gnupg", filepath.Join(".config", "gh")} {
		out = append(out, filepath.Join(home, sub))
	}
	return out
}

// ProtectedPaths are the files under a workspace that a later session trusts
// without asking: project instructions and prompts, and git hooks.
func ProtectedPaths(root string) []string {
	return []string{
		filepath.Join(root, ".liveagent"),
		filepath.Join(root, ".k-brain"),
		filepath.Join(root, ".git", "hooks"),
	}
}
