package sandbox

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// isolatedHome keeps the user's real git configuration out of the tests.
func isolatedHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	return home
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestProtectedPathsPlainRepository(t *testing.T) {
	isolatedHome(t)
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".git", "config"), "[core]\n\thooksPath = .githooks ; shared hooks\n[include]\n\tpath = ../.gitconfig.local\n")
	writeFile(t, filepath.Join(root, ".gitconfig.local"), "[core \"x\"]\n[core]\nhooksPath = \"tools/hooks\"\n")
	got := ProtectedPaths(root)
	for _, want := range []string{
		filepath.Join(root, ".liveagent"),
		filepath.Join(root, ".k-brain"),
		filepath.Join(root, ".git", "hooks"),
		filepath.Join(root, ".git", "config"),
		filepath.Join(root, ".git", "config.worktree"),
		filepath.Join(root, ".githooks"),
		filepath.Join(root, ".gitconfig.local"),
		filepath.Join(root, "tools", "hooks"),
	} {
		if !slices.Contains(got, want) {
			t.Errorf("ProtectedPaths lacks %s: %v", want, got)
		}
	}
	if slices.Contains(got, filepath.Join(root, ".git")) {
		t.Errorf("a .git directory is protected wholesale: %v", got)
	}
}

func TestProtectedPathsLinkedWorktree(t *testing.T) {
	isolatedHome(t)
	main := t.TempDir()
	worktree := t.TempDir()
	gitDir := filepath.Join(main, ".git", "worktrees", "feature")
	writeFile(t, filepath.Join(gitDir, "commondir"), "../..\n")
	writeFile(t, filepath.Join(worktree, ".git"), "gitdir: "+gitDir+"\n")
	got := ProtectedPaths(worktree)
	for _, want := range []string{
		filepath.Join(worktree, ".git"),
		filepath.Join(main, ".git", "hooks"),
		filepath.Join(main, ".git", "config"),
		filepath.Join(gitDir, "config.worktree"),
	} {
		if !slices.Contains(got, want) {
			t.Errorf("ProtectedPaths lacks %s: %v", want, got)
		}
	}
}

func TestProtectedPathsGlobalRelativeHooksPath(t *testing.T) {
	home := isolatedHome(t)
	writeFile(t, filepath.Join(home, ".gitconfig"), "[core]\n\thooksPath = .husky\n")
	root := t.TempDir()
	if got := ProtectedPaths(root); !slices.Contains(got, filepath.Join(root, ".husky")) {
		t.Fatalf("a global relative hooksPath is not protected in the workspace: %v", got)
	}
}

func TestGitConfigValue(t *testing.T) {
	for raw, want := range map[string]string{
		` hooks `:              "hooks",
		` "a b" # comment`:     "a b",
		` "a;b" ; comment`:     "a;b",
		` a\"b`:                `a"b`,
		` ~/hooks`:             "~/hooks",
		` path\\with\\slashes`: `path\with\slashes`,
	} {
		if got := gitConfigValue(raw); got != want {
			t.Errorf("gitConfigValue(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestProtectRefusesPathCoveringTheWorkspace(t *testing.T) {
	root, err := canonicalPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := &Policy{Mode: "workspace", Root: root, Protected: []string{root}}
	if _, err := p.protect(root, []string{root}, false); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("protect with the root itself protected = %v, want a refusal", err)
	}
}

func TestProtectDropsPathsOutsideGrantsAndPinsAncestors(t *testing.T) {
	root, err := canonicalPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	outside, err := canonicalPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".git", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := &Policy{Protected: []string{filepath.Join(root, ".git", "hooks"), filepath.Join(outside, "hooks")}}
	got, err := p.protect(root, []string{root}, true)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.resolved, []string{filepath.Join(root, ".git", "hooks")}) {
		t.Fatalf("protected %v, want only the path inside the grant", got.resolved)
	}
	if !slices.Equal(got.ancestors, []string{filepath.Join(root, ".git")}) {
		t.Fatalf("ancestors %v, want the .git directory", got.ancestors)
	}
}

func TestProtectRefusesSymlinkedPathForBubblewrap(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	root, err := canonicalPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real", filepath.Join(root, ".k-brain")); err != nil {
		t.Fatal(err)
	}
	p := &Policy{Protected: []string{filepath.Join(root, ".k-brain")}}
	if _, err := p.protect(root, []string{root}, true); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("protect through a symlink = %v, want a refusal", err)
	}
	got, err := p.protect(root, []string{root}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(got.paths, filepath.Join(root, ".k-brain")) || !slices.Contains(got.paths, filepath.Join(root, "real")) {
		t.Fatalf("Seatbelt protection %v, want both spellings", got.paths)
	}
}

func TestProtectRefusesHardLinkedFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hard link counts are only checked on Unix")
	}
	root, err := canonicalPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(root, ".git", "config")
	writeFile(t, config, "[core]\n")
	if err := os.Link(config, filepath.Join(root, "innocent")); err != nil {
		t.Skipf("cannot hard link: %v", err)
	}
	p := &Policy{Protected: []string{config}}
	if _, err := p.protect(root, []string{root}, false); err == nil || !strings.Contains(err.Error(), "hard link") {
		t.Fatalf("protect with a hard-linked file = %v, want a refusal", err)
	}
}

func TestForRunProtectsEveryWritableRoot(t *testing.T) {
	isolatedHome(t)
	withProbe(t, Capability{Supported: true, NetworkControl: true})
	root := t.TempDir()
	other, err := canonicalPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p, err := ForRun(nil, RunRequest{Root: root, Writable: []string{other}, Network: true})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(p.Protected, filepath.Join(other, ".git", "hooks")) {
		t.Fatalf("the second writable root is not protected: %v", p.Protected)
	}
}

func TestBwrapArgsPinProtectedAncestors(t *testing.T) {
	isolatedHome(t)
	root, err := canonicalPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, ".git", "config"), "[core]\n")
	if err := os.MkdirAll(filepath.Join(root, ".git", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := &Policy{Mode: "workspace", Root: root, Protected: ProtectedPaths(root)}
	args, err := p.bwrapArgs(root, root)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	dotGit := filepath.Join(root, ".git")
	pin := strings.Index(joined, "--bind "+dotGit+" "+dotGit+" ")
	hooksDir, configFile := filepath.Join(dotGit, "hooks"), filepath.Join(dotGit, "config")
	hooks := strings.Index(joined, "--ro-bind "+hooksDir+" "+hooksDir)
	config := strings.Index(joined, "--ro-bind "+configFile+" "+configFile)
	if pin < 0 || hooks < pin || config < pin {
		t.Fatalf("bwrap args do not pin .git before protecting it: %s", joined)
	}
}

func TestBwrapArgsHideRuntimeSockets(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix sockets on paths are not portable to Windows")
	}
	socket := listenUnix(t)
	t.Setenv("DOCKER_HOST", "unix://"+socket)
	root, err := canonicalPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := &Policy{Mode: "workspace", Root: root}
	args, err := p.bwrapArgs(root, root)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(socket)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(args, " "), "--ro-bind /dev/null "+resolved) {
		t.Fatalf("runtime socket %s not hidden: %q", resolved, args)
	}
}

func TestSeatbeltProfileProtectsAncestorsAndSockets(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("seatbelt profiles hold macOS paths")
	}
	isolatedHome(t)
	t.Setenv("DOCKER_HOST", "unix:///var/run/engine.sock")
	root, err := canonicalPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".git", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := &Policy{Mode: "workspace", Root: root, Network: true, Protected: ProtectedPaths(root)}
	profile, err := p.seatbeltProfile(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`(literal "` + filepath.Join(root, ".git") + `")`,
		`(subpath "` + filepath.Join(root, ".git", "config") + `")`,
		`(remote unix-socket (path-literal "`,
		`engine.sock")`,
	} {
		if !strings.Contains(profile, want) {
			t.Errorf("profile lacks %s:\n%s", want, profile)
		}
	}
	unlink := strings.Index(profile, "(deny file-write-unlink")
	if unlink < 0 || !strings.Contains(profile[unlink:], `(literal "`+filepath.Join(root, ".git")+`")`) {
		t.Errorf("profile does not deny renaming .git:\n%s", profile)
	}
}

// listenUnix serves a Unix socket outside the temporary directories (which
// bubblewrap replaces) and returns its path.
func listenUnix(t *testing.T) string {
	t.Helper()
	dir := outsideTempDir(t)
	path := filepath.Join(dir, "s.sock")
	if len(path) > 100 {
		t.Skipf("socket path too long: %s", path)
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Skipf("cannot listen on a Unix socket: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_, _ = conn.Write([]byte("hello\n"))
			_ = conn.Close()
		}
	}()
	return path
}

func TestLiveSandboxProtectsRepository(t *testing.T) {
	liveSandbox(t)
	// Allow-list mechanisms cannot carve read-only paths out of the workspace.
	if c := Probe(); c.Mechanism == "landlock" {
		t.Skip("this mechanism does not keep protected paths read-only")
	}
	isolatedHome(t)
	root := t.TempDir()
	hook := filepath.Join(root, ".git", "hooks", "pre-commit")
	writeFile(t, hook, "#!/bin/sh\nexit 0\n")
	writeFile(t, filepath.Join(root, ".git", "config"), "[core]\n\tbare = false\n")
	writeFile(t, filepath.Join(root, ".git", "HEAD"), "ref: refs/heads/main\n")
	writeFile(t, filepath.Join(root, ".k-brain", "instructions.md"), "be careful\n")
	p, err := ForRun(nil, RunRequest{Root: root, Network: true})
	if err != nil {
		t.Fatal(err)
	}
	attacks := map[string]string{
		"rename .git away":    `mv .git .git-moved && mkdir -p .git/hooks && echo evil > .git/hooks/pre-commit`,
		"rename hooks away":   `mv .git/hooks .git/hooks-moved && mkdir .git/hooks && echo evil > .git/hooks/pre-commit`,
		"rewrite config":      `printf '[core]\n\thooksPath = evil\n' > .git/config`,
		"replace config":      `rm -f .git/config && echo evil > .git/config`,
		"hard link config":    `ln .git/config config-link && echo evil >> config-link`,
		"write instructions":  `echo evil > .k-brain/instructions.md`,
		"rename instructions": `mv .k-brain .k-brain-moved && mkdir .k-brain && echo evil > .k-brain/instructions.md`,
		"rename the root out": `cd .. && mv ` + filepath.Base(root) + ` moved-root && mkdir -p ` + filepath.Base(root) + `/.git/hooks`,
	}
	for name, script := range attacks {
		_, _ = confinedRun(t, p, script)
		if data, err := os.ReadFile(hook); err != nil || !strings.Contains(string(data), "exit 0") {
			t.Fatalf("%s: hook changed: %q %v", name, data, err)
		}
		if data, err := os.ReadFile(filepath.Join(root, ".git", "config")); err != nil || strings.Contains(string(data), "evil") {
			t.Fatalf("%s: config changed: %q %v", name, data, err)
		}
		if data, err := os.ReadFile(filepath.Join(root, ".k-brain", "instructions.md")); err != nil || strings.Contains(string(data), "evil") {
			t.Fatalf("%s: instructions changed: %q %v", name, data, err)
		}
	}
	// bubblewrap can only bind what exists; Seatbelt rules also cover a
	// protected path that does not exist yet.
	if runtime.GOOS == "darwin" {
		_, _ = confinedRun(t, p, `mkdir -p .liveagent && echo evil > .liveagent/prompt.md`)
		if _, err := os.Stat(filepath.Join(root, ".liveagent")); err == nil {
			t.Fatal("missing protected directory created")
		}
	}
	// Ordinary repository writes still work.
	if out, err := confinedRun(t, p, `echo x > .git/HEAD.lock && mv .git/HEAD.lock .git/ORIG_HEAD && mkdir -p .git/refs/heads`); err != nil {
		t.Fatalf("ordinary .git write failed: %q %v", out, err)
	}
}

func TestLiveSandboxHidesRuntimeSockets(t *testing.T) {
	liveSandbox(t)
	if c := Probe(); c.Mechanism == "landlock" {
		t.Skip("this mechanism cannot hide sockets on paths")
	}
	if runtime.GOOS == "windows" {
		t.Skip("Unix sockets on paths are not portable to Windows")
	}
	hidden := listenUnix(t)
	control := listenUnix(t)
	t.Setenv("DOCKER_HOST", "unix://"+hidden)
	// bubblewrap replaces /tmp, where the test binary usually lives.
	self := copyTestBinary(t)
	for _, network := range []bool{true, false} {
		root := t.TempDir()
		p, err := ForRun(nil, RunRequest{Root: root, Network: network})
		if err != nil {
			t.Fatal(err)
		}
		connect := func(path string) string {
			cmd := exec.Command(self, "-test.run=^TestSocketConnectHelper$")
			cmd.Dir = root
			wrapped, err := p.Wrap(context.Background(), cmd)
			if err != nil {
				t.Fatal(err)
			}
			wrapped.Env = append(os.Environ(), "KBRAIN_SOCKET_HELPER="+path)
			out, _ := wrapped.CombinedOutput()
			return string(out)
		}
		if out := connect(hidden); strings.Contains(out, "connected") {
			t.Fatalf("network=%v: runtime socket reachable: %s", network, out)
		}
		if network {
			if out := connect(control); !strings.Contains(out, "connected") {
				t.Fatalf("an ordinary socket is unreachable, so the check above proves nothing: %s", out)
			}
		}
	}
}

func TestSocketConnectHelper(t *testing.T) {
	path := os.Getenv("KBRAIN_SOCKET_HELPER")
	if path == "" {
		t.Skip("runs only inside TestLiveSandboxHidesRuntimeSockets")
	}
	conn, err := net.Dial("unix", path)
	if err != nil {
		fmt.Println("refused:", err)
		return
	}
	_ = conn.Close()
	fmt.Println("connected")
}

func copyTestBinary(t *testing.T) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(outsideTempDir(t), "sandbox.test")
	if err := os.WriteFile(path, data, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}
