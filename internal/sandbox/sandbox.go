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
}

func (p *Policy) ForRoot(root string) *Policy {
	if p == nil {
		return nil
	}
	copy := *p
	copy.Root = root
	copy.Writable = append([]string(nil), p.Writable...)
	copy.ReadOnly = append([]string(nil), p.ReadOnly...)
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

func (p *Policy) Wrap(ctx context.Context, cmd *exec.Cmd) (*exec.Cmd, error) {
	if !p.Enabled() {
		return cmd, nil
	}
	if p.Root == "" {
		return nil, fmt.Errorf("sandbox root is empty")
	}
	root, err := filepath.Abs(p.Root)
	if err != nil {
		return nil, err
	}
	if runtime.GOOS == "linux" {
		return p.wrapBubblewrap(ctx, cmd, root)
	}
	if runtime.GOOS == "darwin" {
		return p.wrapSandboxExec(ctx, cmd, root)
	}
	if runtime.GOOS == "windows" {
		return p.wrapWindowsWSL(ctx, cmd, root)
	}
	return nil, fmt.Errorf("sandbox backend %q is unavailable on Windows; run inside WSL2 with bubblewrap or set sandbox.mode to off", p.Backend)
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
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		return nil, fmt.Errorf("sandbox requires bubblewrap (bwrap): %w", err)
	}
	args := []string{"--die-with-parent", "--new-session", "--unshare-pid", "--ro-bind", "/", "/", "--dev", "/dev", "--proc", "/proc", "--tmpfs", "/tmp"}
	if !p.Network {
		args = append(args, "--unshare-net")
	}
	writable := append([]string{root}, p.Writable...)
	for _, path := range writable {
		path, err = resolvePath(root, path)
		if err != nil {
			return nil, err
		}
		args = append(args, "--bind", path, path)
	}
	for _, path := range p.ReadOnly {
		path, err = resolvePath(root, path)
		if err != nil {
			return nil, err
		}
		args = append(args, "--ro-bind", path, path)
	}
	dir := commandDir(cmd, root)
	args = append(args, "--chdir", dir, "--", cmd.Path)
	args = append(args, cmd.Args[1:]...)
	wrapped := exec.CommandContext(ctx, bwrap, args...)
	wrapped.Env = cmd.Env
	wrapped.Dir = dir
	return wrapped, nil
}

func (p *Policy) wrapSandboxExec(ctx context.Context, cmd *exec.Cmd, root string) (*exec.Cmd, error) {
	backend := strings.ToLower(strings.TrimSpace(p.Backend))
	if backend != "" && backend != "auto" && backend != "seatbelt" {
		return nil, fmt.Errorf("unsupported macOS sandbox backend %q", p.Backend)
	}
	sandboxExec, err := exec.LookPath("sandbox-exec")
	if err != nil {
		return nil, fmt.Errorf("sandbox requires sandbox-exec: %w", err)
	}
	profile := `(version 1) (deny default) (allow process*) (allow file-read*)`
	profile += ` (allow file-write* (subpath "` + profilePath(root) + `"))`
	for _, path := range p.Writable {
		resolved, err := resolvePath(root, path)
		if err != nil {
			return nil, err
		}
		profile += ` (allow file-write* (subpath "` + profilePath(resolved) + `"))`
	}
	if p.Network {
		profile += ` (allow network*)`
	}
	args := []string{"-p", profile, cmd.Path}
	args = append(args, cmd.Args[1:]...)
	wrapped := exec.CommandContext(ctx, sandboxExec, args...)
	wrapped.Env = cmd.Env
	wrapped.Dir = commandDir(cmd, root)
	return wrapped, nil
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
