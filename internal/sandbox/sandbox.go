package sandbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
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
	// EnvKeep lists variable names (globs allowed) a sandboxed command still
	// inherits although they look like credentials (FilterEnv).
	EnvKeep []string
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
	copy.EnvKeep = append([]string(nil), p.EnvKeep...)
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
	// Every backend hands cmd.Env to the wrapped command; nil would inherit
	// this process's environment, credentials included.
	env := cmd.Env
	if env == nil {
		env = os.Environ()
	}
	cmd.Env = p.FilterEnv(env)
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
		return p.wrapWindows(ctx, cmd, root)
	}
	return nil, fmt.Errorf("sandbox backend %q is unavailable on %s; set sandbox.mode to off", p.Backend, runtime.GOOS)
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
	grants := append([]string{root}, writable...)
	for _, path := range grants {
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
	protected, err := p.protect(root, grants, true)
	if err != nil {
		return nil, err
	}
	// A mount point cannot be renamed (EBUSY), so binding each directory
	// between a grant and a protected path onto itself keeps a command from
	// moving the protected path away and creating its own in its place.
	for _, dir := range protected.ancestors {
		args = append(args, "--bind", dir, dir)
	}
	readOnly, err := resolveAll(root, p.ReadOnly)
	if err != nil {
		return nil, err
	}
	for _, path := range appendNew(readOnly, protected.resolved...) {
		// bubblewrap cannot bind a missing source; a path that does not exist
		// yet is left to the root's write grant.
		if _, statErr := os.Lstat(path); statErr == nil {
			args = append(args, "--ro-bind", path, path)
		}
	}
	// Binding an empty file over a socket makes connect fail.
	for _, socket := range existingSockets(runtimeSockets("linux")) {
		if !slices.ContainsFunc(masked, func(dir string) bool { return encloses(dir, socket) }) {
			args = append(args, "--ro-bind", "/dev/null", socket)
		}
	}
	if isDir(wslInteropDir) {
		args = append(args, "--tmpfs", wslInteropDir)
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
	readOnly, err := resolveAll(root, p.ReadOnly)
	if err != nil {
		return "", err
	}
	grants := append([]string{root}, writable...)
	// Masks are denied after the grants, so only the home directory has to stay
	// out of the temporary grants.
	grants = append(grants, tempDirs([]string{homeDir()})...)
	protected, err := p.protect(root, grants, false)
	if err != nil {
		return "", err
	}
	// Seatbelt checks the resolved path, so a symlinked protected path is
	// denied under both spellings: the link itself and its target.
	readOnly = appendNew(readOnly, protected.paths...)
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
		// file-link: a hard link elsewhere would be a writable second name.
		fmt.Fprintf(&b, "(deny file-write* file-link %s)\n", subpaths(readOnly))
	}
	if ancestors := protectedAncestors(protected.paths, grants); len(ancestors) > 0 {
		// Renaming a directory above a protected path would carry it to a
		// writable name and let a replacement take its place.
		fmt.Fprintf(&b, "(deny file-write-unlink %s)\n", literals(ancestors))
	}
	if !p.Network {
		b.WriteString("(deny network*)\n")
	} else {
		var sockets []string
		for _, socket := range runtimeSockets("darwin") {
			if resolved, err := canonicalPath(socket); err == nil {
				sockets = appendNew(sockets, resolved)
			}
		}
		if len(sockets) > 0 {
			parts := make([]string, len(sockets))
			for i, socket := range sockets {
				parts[i] = `(remote unix-socket (path-literal "` + profilePath(socket) + `"))`
			}
			fmt.Fprintf(&b, "(deny network-outbound %s)\n", strings.Join(parts, " "))
		}
	}
	return b.String(), nil
}

// protectedAncestors lists the directories above a protected path that a
// command could rename: those whose parent is writable. That includes the
// workspace itself when a temporary directory holds it.
func protectedAncestors(paths, grants []string) []string {
	var out []string
	for _, path := range paths {
		for dir := filepath.Dir(path); filepath.Dir(dir) != dir; dir = filepath.Dir(dir) {
			if enclosingGrant(grants, filepath.Dir(dir)) != "" {
				out = appendNew(out, dir)
			}
		}
	}
	return out
}

func literals(paths []string) string {
	parts := make([]string, len(paths))
	for i, path := range paths {
		parts[i] = `(literal "` + profilePath(path) + `")`
	}
	return strings.Join(parts, " ")
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
