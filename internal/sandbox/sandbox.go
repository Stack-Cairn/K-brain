package sandbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

type Policy struct {
	Mode     string
	Backend  string
	Root     string
	Network  bool
	Writable []string
	ReadOnly []string
	// Masked directories are neither readable nor writable inside the sandbox
	// (credentials, the backend's own data). A workspace root inside one of them
	// stays usable; a root that encloses one is refused.
	Masked []string
	// Protected paths stay readable but not writable, even inside a writable
	// root: project instructions and hooks that a later, unsandboxed session
	// would trust.
	Protected []string
}

func (p *Policy) ForRoot(root string) *Policy {
	if p == nil {
		return nil
	}
	copy := *p
	copy.Root = root
	copy.Writable = append([]string(nil), p.Writable...)
	copy.ReadOnly = append([]string(nil), p.ReadOnly...)
	copy.Masked = append([]string(nil), p.Masked...)
	copy.Protected = append([]string(nil), p.Protected...)
	return &copy
}

type contextKey struct{}

func WithPolicy(ctx context.Context, policy *Policy) context.Context {
	return context.WithValue(ctx, contextKey{}, policy)
}

func FromContext(ctx context.Context) *Policy {
	if ctx == nil {
		return nil
	}
	policy, _ := ctx.Value(contextKey{}).(*Policy)
	return policy
}

func (p *Policy) Enabled() bool {
	return p != nil && p.Mode != "" && p.Mode != "off" && p.Mode != "disabled"
}

// Wrap returns cmd confined by the policy. It fails rather than run the command
// unconfined when the platform mechanism is missing or the root is unsafe.
func (p *Policy) Wrap(ctx context.Context, cmd *exec.Cmd) (*exec.Cmd, error) {
	if !p.Enabled() {
		return cmd, nil
	}
	if p.Root == "" {
		return nil, fmt.Errorf("sandbox root is empty")
	}
	root, err := canonicalPath(p.Root)
	if err != nil {
		return nil, err
	}
	if err := checkRoot(root, p.Masked); err != nil {
		return nil, err
	}
	if runtime.GOOS == "linux" {
		return p.wrapLinux(ctx, cmd, root)
	}
	if runtime.GOOS == "darwin" {
		return p.wrapSandboxExec(ctx, cmd, root)
	}
	if runtime.GOOS == "windows" {
		return p.wrapWindowsWSL(ctx, cmd, root)
	}
	return nil, fmt.Errorf("sandbox backend %q is unavailable on %s; set sandbox.mode to off", p.Backend, runtime.GOOS)
}

func (p *Policy) wrapWindowsWSL(ctx context.Context, cmd *exec.Cmd, root string) (*exec.Cmd, error) {
	backend := strings.ToLower(strings.TrimSpace(p.Backend))
	if backend != "wsl" && backend != "bubblewrap" && backend != "auto" && backend != "" {
		return nil, fmt.Errorf("unsupported Windows sandbox backend %q; use wsl", p.Backend)
	}
	wsl, err := exec.LookPath("wsl.exe")
	if err != nil {
		return nil, fmt.Errorf("sandbox requires WSL2 with bubblewrap: %w", err)
	}
	dir := commandDir(cmd, root)
	dirLinux, err := windowsToWSLPath(dir)
	if err != nil {
		return nil, err
	}
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		if _, err = exec.LookPath("wsl.exe"); err != nil {
			return nil, fmt.Errorf("sandbox requires bubblewrap inside WSL2: %w", err)
		}
		bwrap = "bwrap"
	}
	args := []string{"--", bwrap, "--die-with-parent", "--new-session", "--unshare-pid", "--ro-bind", "/", "/", "--dev", "/dev", "--proc", "/proc", "--tmpfs", "/tmp"}
	if !p.Network {
		args = append(args, "--unshare-net")
	}
	for _, path := range append([]string{root}, p.Writable...) {
		resolved, pathErr := resolvePath(root, path)
		if pathErr != nil {
			return nil, pathErr
		}
		linuxPath, pathErr := windowsToWSLPath(resolved)
		if pathErr != nil {
			return nil, pathErr
		}
		args = append(args, "--bind", linuxPath, linuxPath)
	}
	for _, path := range p.ReadOnly {
		resolved, pathErr := resolvePath(root, path)
		if pathErr != nil {
			return nil, pathErr
		}
		linuxPath, pathErr := windowsToWSLPath(resolved)
		if pathErr != nil {
			return nil, pathErr
		}
		args = append(args, "--ro-bind", linuxPath, linuxPath)
	}
	program := cmd.Path
	if converted, convErr := windowsToWSLPath(program); convErr == nil {
		program = converted
	}
	args = append(args, "--chdir", dirLinux, "--", program)
	args = append(args, cmd.Args[1:]...)
	w := exec.CommandContext(ctx, wsl, args...)
	w.Dir = dir
	w.Env = cmd.Env
	return w, nil
}

func windowsToWSLPath(path string) (string, error) {
	if strings.HasPrefix(path, `\\`) {
		return "", fmt.Errorf("UNC sandbox roots are not supported: %s", path)
	}
	vol := filepath.VolumeName(path)
	if len(vol) == 2 && vol[1] == ':' {
		rest := filepath.ToSlash(strings.TrimPrefix(path, vol))
		return "/mnt/" + strings.ToLower(string(vol[0])) + rest, nil
	}
	return filepath.ToSlash(path), nil
}

func (p *Policy) wrapBubblewrap(ctx context.Context, cmd *exec.Cmd, root string) (*exec.Cmd, error) {
	backend := strings.ToLower(strings.TrimSpace(p.Backend))
	if backend != "" && backend != "auto" && backend != "bwrap" && backend != "bubblewrap" {
		return nil, fmt.Errorf("unsupported Linux sandbox backend %q", p.Backend)
	}
	bwrap := resolveBwrap(os.Getenv("PATH"), root)
	if bwrap == "" {
		return nil, fmt.Errorf("sandbox requires bubblewrap (bwrap); install it, e.g. `apt install bubblewrap`")
	}
	args, err := p.bwrapArgs(root, commandDir(cmd, root))
	if err != nil {
		return nil, err
	}
	args = append(args, "--", cmd.Path)
	args = append(args, cmd.Args[1:]...)
	wrapped := exec.CommandContext(ctx, bwrap, args...)
	wrapped.Env = cmd.Env
	wrapped.Dir = commandDir(cmd, root)
	return wrapped, nil
}

// bwrapArgs mounts the host read-only, then grants writes to the root and the
// extra writable paths, hides masked directories behind empty tmpfs mounts and
// binds read-only paths over writable ones. bubblewrap applies the mounts in
// order, so a root inside a masked directory is bound back after the mask.
func (p *Policy) bwrapArgs(root, dir string) ([]string, error) {
	args := []string{"--die-with-parent", "--new-session", "--unshare-pid", "--ro-bind", "/", "/", "--dev", "/dev", "--proc", "/proc", "--tmpfs", "/tmp"}
	if !p.Network {
		args = append(args, "--unshare-net")
	}
	writable, err := resolveAll(root, p.Writable)
	if err != nil {
		return nil, err
	}
	for _, path := range append([]string{root}, writable...) {
		args = append(args, "--bind", path, path)
	}
	masked, err := resolveAll(root, p.Masked)
	if err != nil {
		return nil, err
	}
	rebind := false
	for _, path := range masked {
		if !isDir(path) {
			continue
		}
		args = append(args, "--tmpfs", path)
		rebind = rebind || encloses(path, root)
	}
	if rebind {
		args = append(args, "--bind", root, root)
	}
	readOnly, err := resolveAll(root, append(append([]string(nil), p.ReadOnly...), p.Protected...))
	if err != nil {
		return nil, err
	}
	for _, path := range readOnly {
		// bubblewrap cannot bind a missing source; a path that does not exist
		// yet is left to the root's write grant.
		if _, statErr := os.Lstat(path); statErr == nil {
			args = append(args, "--ro-bind", path, path)
		}
	}
	return append(args, "--chdir", dir), nil
}

func (p *Policy) wrapSandboxExec(ctx context.Context, cmd *exec.Cmd, root string) (*exec.Cmd, error) {
	backend := strings.ToLower(strings.TrimSpace(p.Backend))
	if backend != "" && backend != "auto" && backend != "seatbelt" {
		return nil, fmt.Errorf("unsupported macOS sandbox backend %q", p.Backend)
	}
	if _, err := os.Stat(sandboxExecPath); err != nil {
		return nil, fmt.Errorf("sandbox requires %s: %w", sandboxExecPath, err)
	}
	profile, err := p.seatbeltProfile(root)
	if err != nil {
		return nil, err
	}
	args := []string{"-p", profile, cmd.Path}
	args = append(args, cmd.Args[1:]...)
	wrapped := exec.CommandContext(ctx, sandboxExecPath, args...)
	wrapped.Env = cmd.Env
	wrapped.Dir = commandDir(cmd, root)
	return wrapped, nil
}

const sandboxExecPath = "/usr/bin/sandbox-exec"

// seatbeltProfile allows everything except writes, then grants writes to the
// root, the extra writable paths, the temporary directories and the device
// nodes shells need. Seatbelt applies the last matching rule, so the order is:
// write fence, write grants, masks, the root re-allowed (it may sit inside a
// mask), read-only paths and finally the optional network deny. Reads stay open
// outside the masks: toolchains and dependencies live all over the disk.
func (p *Policy) seatbeltProfile(root string) (string, error) {
	writable, err := resolveAll(root, p.Writable)
	if err != nil {
		return "", err
	}
	masked, err := resolveAll(root, p.Masked)
	if err != nil {
		return "", err
	}
	readOnly, err := resolveAll(root, append(append([]string(nil), p.ReadOnly...), p.Protected...))
	if err != nil {
		return "", err
	}
	grants := append([]string{root}, writable...)
	// Masks are denied after the grants, so only the home directory has to stay
	// out of the temporary grants.
	grants = append(grants, tempDirs([]string{homeDir()})...)
	var b strings.Builder
	b.WriteString("(version 1)\n(allow default)\n(deny file-write*)\n")
	fmt.Fprintf(&b, "(allow file-write* %s)\n", subpaths(grants))
	b.WriteString(`(allow file-write-data file-ioctl (literal "/dev/null") (literal "/dev/zero") (literal "/dev/tty") (literal "/dev/stdout") (literal "/dev/stderr") (literal "/dev/dtracehelper"))` + "\n")
	b.WriteString(`(allow file-write* (subpath "/dev/fd"))` + "\n")
	if len(masked) > 0 {
		fmt.Fprintf(&b, "(deny file-read* file-write* %s)\n", subpaths(masked))
		fmt.Fprintf(&b, "(allow file-read* file-write* %s)\n", subpaths([]string{root}))
	}
	if len(readOnly) > 0 {
		fmt.Fprintf(&b, "(deny file-write* %s)\n", subpaths(readOnly))
	}
	if !p.Network {
		b.WriteString("(deny network*)\n")
	}
	return b.String(), nil
}

func subpaths(paths []string) string {
	parts := make([]string, len(paths))
	for i, path := range paths {
		parts[i] = `(subpath "` + profilePath(path) + `")`
	}
	return strings.Join(parts, " ")
}

func commandDir(cmd *exec.Cmd, root string) string {
	if cmd.Dir == "" {
		return root
	}
	if filepath.IsAbs(cmd.Dir) {
		return cmd.Dir
	}
	return filepath.Join(root, cmd.Dir)
}

func resolvePath(root, path string) (string, error) {
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	return filepath.Abs(path)
}

func profilePath(path string) string {
	path = filepath.Clean(path)
	return strings.ReplaceAll(strings.ReplaceAll(path, `\`, `\\`), `"`, `\"`)
}

func New(mode, backend, root string, network bool, writable, readOnly []string) *Policy {
	if mode == "" || mode == "off" || mode == "disabled" {
		return nil
	}
	return &Policy{Mode: mode, Backend: backend, Root: root, Network: network, Writable: writable, ReadOnly: readOnly}
}

func RootFromEnv() string {
	if root := os.Getenv("K_BRAIN_SANDBOX_ROOT"); root != "" {
		return root
	}
	root, _ := os.Getwd()
	return root
}
