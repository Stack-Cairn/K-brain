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
		filepath.Join(root, ".git", "commondir"),
		filepath.Join(root, ".git", "info", "attributes"),
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

func TestProtectedPathsSubmodulesAndWorktrees(t *testing.T) {
	isolatedHome(t)
	root := t.TempDir()
	dotGit := filepath.Join(root, ".git")
	writeFile(t, filepath.Join(dotGit, "config"), "[core]\n")
	writeFile(t, filepath.Join(root, ".gitmodules"), "[submodule \"lib\"]\n\tpath = vendor/lib\n\turl = ../lib\n")
	gitDir := func(dir, config string) {
		writeFile(t, filepath.Join(dir, "HEAD"), "ref: refs/heads/main\n")
		writeFile(t, filepath.Join(dir, "config"), config)
		if err := os.MkdirAll(filepath.Join(dir, "objects"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	lib := filepath.Join(dotGit, "modules", "lib")
	gitDir(lib, "[core]\n\tworktree = ../../../vendor/lib\n\thooksPath = tools/hooks\n")
	writeFile(t, filepath.Join(root, "vendor", "lib", ".git"), "gitdir: ../../.git/modules/lib\n")
	// A nested submodule, and one whose name contains a slash.
	nested := filepath.Join(lib, "modules", "inner")
	gitDir(nested, "[core]\n\tworktree = ../../../../../vendor/lib/inner\n")
	writeFile(t, filepath.Join(root, "vendor", "lib", "inner", ".git"), "gitdir: ../../../.git/modules/lib/modules/inner\n")
	slashed := filepath.Join(dotGit, "modules", "group", "tool")
	gitDir(slashed, "[core]\n")
	// A linked worktree registered in the main repository.
	admin := filepath.Join(dotGit, "worktrees", "feature")
	writeFile(t, filepath.Join(admin, "HEAD"), "ref: refs/heads/feature\n")
	writeFile(t, filepath.Join(admin, "commondir"), "../..\n")
	got := ProtectedPaths(root)
	want := []string{
		filepath.Join(dotGit, "commondir"),
		filepath.Join(dotGit, "info", "attributes"),
		filepath.Join(admin, "commondir"),
		filepath.Join(admin, "config.worktree"),
		filepath.Join(root, "vendor", "lib", ".git"),
		filepath.Join(root, "vendor", "lib", "inner", ".git"),
		filepath.Join(root, "vendor", "lib", "tools", "hooks"),
	}
	for _, dir := range []string{lib, nested, slashed} {
		want = append(want, filepath.Join(dir, "config"), filepath.Join(dir, "hooks"), filepath.Join(dir, "commondir"), filepath.Join(dir, "info", "attributes"))
	}
	for _, path := range want {
		if !slices.Contains(got, path) {
			t.Errorf("ProtectedPaths lacks %s: %v", path, got)
		}
	}
	for _, path := range []string{filepath.Join(dotGit, "modules"), filepath.Join(dotGit, "modules", "group"), filepath.Join(dotGit, "worktrees")} {
		if slices.Contains(got, path) {
			t.Errorf("%s is protected wholesale: %v", path, got)
		}
	}
}

func TestProtectedPathsLinkedWorktreeCommondir(t *testing.T) {
	isolatedHome(t)
	main := t.TempDir()
	worktree := t.TempDir()
	gitDir := filepath.Join(main, ".git", "worktrees", "feature")
	writeFile(t, filepath.Join(gitDir, "commondir"), "../..\n")
	writeFile(t, filepath.Join(worktree, ".git"), "gitdir: "+gitDir+"\n")
	got := ProtectedPaths(worktree)
	for _, want := range []string{filepath.Join(gitDir, "commondir"), filepath.Join(main, ".git", "info", "attributes")} {
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
	// The Landlock launcher keeps protected paths only where its supervisor
	// can run (see supervise_linux.go).
	if c := Probe(); !c.PathProtection {
		t.Skip("this mechanism does not keep protected paths read-only here")
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
	// bubblewrap can only bind what exists; Seatbelt rules and the Landlock
	// supervisor also cover a protected path that does not exist yet.
	if runtime.GOOS == "darwin" || Probe().Mechanism == mechanismLandlock {
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

// gitRepoWithSubmoduleAndWorktree builds, with the real git and outside the
// sandbox, a repository under root with a submodule at sub/ and a linked
// worktree at wt/, so the git directories hold what git itself writes.
func gitRepoWithSubmoduleAndWorktree(t *testing.T, root string) {
	t.Helper()
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command(gitBin, append([]string{"-c", "protocol.file.allow=always", "-c", "init.defaultBranch=main"}, args...)...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	upstream := filepath.Join(root, "upstream")
	run(root, "init", "-q", upstream)
	run(upstream, "commit", "-q", "--allow-empty", "-m", "lib")
	repo := filepath.Join(root, "repo")
	run(root, "init", "-q", repo)
	run(repo, "commit", "-q", "--allow-empty", "-m", "init")
	// A file:// URL keeps git from hard-linking the clone's files.
	url := "file://" + filepath.ToSlash(upstream)
	if runtime.GOOS == "windows" {
		url = "file:///" + filepath.ToSlash(upstream)
	}
	run(repo, "submodule", "add", "-q", url, "sub")
	run(repo, "commit", "-q", "-m", "add sub")
	run(repo, "worktree", "add", "-q", "wt", "-b", "feature")
	// Entries git does not create by itself, harmless as written: an empty
	// attributes file and a worktree configuration git ignores without
	// extensions.worktreeConfig.
	writeFile(t, filepath.Join(repo, ".git", "info", "attributes"), "# none\n")
	writeFile(t, filepath.Join(repo, ".git", "worktrees", "wt", "config.worktree"), "")
	writeFile(t, filepath.Join(repo, ".git", "modules", "sub", "hooks", "pre-commit"), "#!/bin/sh\nexit 0\n")
}

// entryState describes a path for before/after comparison: its type and its
// content, link target or (for a directory) its files' contents.
func entryState(path string) string {
	info, err := os.Lstat(path)
	if err != nil {
		return "missing"
	}
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		target, _ := os.Readlink(path)
		return "symlink:" + target
	case info.IsDir():
		var b strings.Builder
		b.WriteString("dir")
		_ = filepath.WalkDir(path, func(file string, entry os.DirEntry, err error) error {
			if err == nil && file != path {
				rel, _ := filepath.Rel(path, file)
				b.WriteString("|" + rel + "=" + entryState(file))
			}
			return nil
		})
		return b.String()
	default:
		data, _ := os.ReadFile(path)
		return fmt.Sprintf("file:%o:%q", info.Mode().Perm(), data)
	}
}

func TestLiveSandboxProtectsGitRedirects(t *testing.T) {
	liveSandbox(t)
	if runtime.GOOS == "windows" {
		t.Skip("the Windows protected-path tests run the Windows helper")
	}
	if c := Probe(); !c.PathProtection {
		t.Skip("this mechanism does not keep protected paths read-only here")
	}
	isolatedHome(t)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, key := range []string{"GIT_AUTHOR_NAME", "GIT_COMMITTER_NAME"} {
		t.Setenv(key, "sandbox")
	}
	for _, key := range []string{"GIT_AUTHOR_EMAIL", "GIT_COMMITTER_EMAIL"} {
		t.Setenv(key, "sandbox@example.invalid")
	}
	base, err := canonicalPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	gitRepoWithSubmoduleAndWorktree(t, base)
	root := filepath.Join(base, "repo")
	p, err := ForRun(nil, RunRequest{Root: root, Network: true})
	if err != nil {
		t.Fatal(err)
	}
	existingFiles := []string{
		".git/modules/sub/config",
		".git/worktrees/wt/commondir",
		".git/worktrees/wt/config.worktree",
		".git/info/attributes",
		"sub/.git",
	}
	existingDirs := []string{".git/modules/sub/hooks"}
	// Missing entries: the main example of #171 and its submodule and
	// worktree counterparts.
	missing := []string{".git/commondir", ".git/modules/sub/commondir", ".git/modules/sub/info/attributes"}
	for _, rel := range append(append(append([]string(nil), existingFiles...), existingDirs...), missing...) {
		if !slices.Contains(p.Protected, filepath.Join(root, filepath.FromSlash(rel))) {
			t.Fatalf("%s is not protected: %v", rel, p.Protected)
		}
	}
	reserves := runtime.GOOS == "darwin" || Probe().Mechanism == mechanismLandlock
	watched := append(append([]string(nil), existingFiles...), existingDirs...)
	if reserves {
		watched = append(watched, missing...)
	}
	before := map[string]string{}
	for _, rel := range watched {
		before[rel] = entryState(filepath.Join(root, filepath.FromSlash(rel)))
	}
	if before[".git/worktrees/wt/commondir"] == "missing" || before["sub/.git"] == "missing" {
		t.Fatalf("git did not lay out the repository as expected: %v", before)
	}
	type attack struct{ name, script string }
	var attacks []attack
	for _, f := range existingFiles {
		attacks = append(attacks,
			attack{"write " + f, `printf '../../evil\n' > ` + f},
			attack{"append " + f, `printf 'evil\n' >> ` + f},
			attack{"delete " + f, `rm -f ` + f},
			attack{"rename " + f, `mv ` + f + ` moved-entry`},
			attack{"rename over " + f, `printf evil > evil-entry && mv -f evil-entry ` + f},
			attack{"replace " + f + " with a symlink", `ln -sf ../evil ` + f},
			attack{"delete and symlink " + f, `rm -f ` + f + `; ln -s ../evil ` + f},
		)
	}
	for _, d := range existingDirs {
		attacks = append(attacks,
			attack{"write into " + d, `printf evil > ` + d + `/pre-commit`},
			attack{"create in " + d, `printf evil > ` + d + `/post-checkout`},
			attack{"delete " + d, `rm -rf ` + d},
			attack{"rename " + d, `mv ` + d + ` moved-hooks && mkdir ` + d + ` && printf evil > ` + d + `/pre-commit`},
			attack{"replace " + d + " with a symlink", `rm -rf ` + d + `; ln -s ../../../evil ` + d},
		)
	}
	if reserves {
		for _, f := range missing {
			attacks = append(attacks,
				attack{"create " + f, `printf '../../evil\n' > ` + f},
				attack{"mkdir " + f, `mkdir -p ` + f},
				attack{"symlink " + f, `ln -s ../evil ` + f},
				attack{"rename into " + f, `printf '../../evil\n' > evil-entry && mv -f evil-entry ` + f},
				attack{"hard link into " + f, `printf '../../evil\n' > evil-entry && ln evil-entry ` + f},
			)
		}
	}
	for _, a := range attacks {
		out, _ := confinedRun(t, p, a.script)
		for _, rel := range watched {
			if now := entryState(filepath.Join(root, filepath.FromSlash(rel))); now != before[rel] {
				t.Fatalf("%s changed %s:\nbefore %s\nafter  %s\noutput %s", a.name, rel, before[rel], now, out)
			}
		}
	}
	// Ordinary git use inside the sandbox still works: commits in the
	// superproject, the submodule and the linked worktree, a branch, a status
	// that recurses into the submodule and a new worktree.
	for _, script := range []string{
		`git status --porcelain >/dev/null`,
		`printf x > file.txt && git add file.txt && git commit -q -m one`,
		`git checkout -q -b topic && printf y >> file.txt && git commit -q -am two && git checkout -q main`,
		`git branch -q other && git branch --list other | grep -q other`,
		`cd sub && printf s > s.txt && git add s.txt && git commit -q -m sub`,
		`cd wt && printf w > w.txt && git add w.txt && git commit -q -m wt`,
		`git worktree add -q wt2 -b feature2 && test -f wt2/.git`,
	} {
		if out, err := confinedRun(t, p, script); err != nil {
			t.Fatalf("ordinary git use failed: %s: %v\n%s", script, err, out)
		}
	}
	for _, rel := range watched {
		if now := entryState(filepath.Join(root, filepath.FromSlash(rel))); now != before[rel] {
			t.Fatalf("ordinary git use changed %s:\nbefore %s\nafter  %s", rel, before[rel], now)
		}
	}
	cmd := exec.Command("git", "rev-parse", "--git-common-dir")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != ".git" {
		t.Fatalf("git outside the sandbox reads its common directory from %q (%v)", out, err)
	}
}
