//go:build linux

package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// protectOnly skips a test when the launcher cannot keep protected paths on
// this kernel. KBRAIN_EXPECT_PATH_PROTECTION=1 (the CI Landlock lane) turns
// that into a failure, so the lane cannot pass by skipping.
func protectOnly(t *testing.T) {
	t.Helper()
	landlockOnly(t)
	if err := landlockProtection(); err != nil {
		if os.Getenv("KBRAIN_EXPECT_PATH_PROTECTION") == "1" {
			t.Fatalf("protected paths are unavailable under Landlock but KBRAIN_EXPECT_PATH_PROTECTION=1: %v", err)
		}
		t.Skipf("protected paths are unavailable under Landlock here: %v", err)
	}
}

func TestFsCallNumbers(t *testing.T) {
	arch, ok := seccompArches[runtime.GOARCH]
	if !ok {
		t.Skipf("no seccomp filter for %s", runtime.GOARCH)
	}
	fs := arch.fs
	want := map[string][2]uint32{
		"openat":    {fs.openat, unix.SYS_OPENAT},
		"openat2":   {fs.openat2, unix.SYS_OPENAT2},
		"mkdirat":   {fs.mkdirat, unix.SYS_MKDIRAT},
		"mknodat":   {fs.mknodat, unix.SYS_MKNODAT},
		"unlinkat":  {fs.unlinkat, unix.SYS_UNLINKAT},
		"renameat2": {fs.renameat2, unix.SYS_RENAMEAT2},
		"linkat":    {fs.linkat, unix.SYS_LINKAT},
		"symlinkat": {fs.symlinkat, unix.SYS_SYMLINKAT},
		"truncate":  {fs.truncate, unix.SYS_TRUNCATE},
		"fchmod":    {fs.fchmod, unix.SYS_FCHMOD},
		"fchmodat":  {fs.fchmodat, unix.SYS_FCHMODAT},
		"fchmodat2": {fs.fchmodat2, unix.SYS_FCHMODAT2},
		"fchown":    {fs.fchown, unix.SYS_FCHOWN},
		"fchownat":  {fs.fchownat, unix.SYS_FCHOWNAT},
		"setxattr":  {arch.xattrSet[0], unix.SYS_SETXATTR},
	}
	for name, pair := range want {
		if pair[0] != pair[1] {
			t.Errorf("%s: table has %d, the kernel ABI %d", name, pair[0], pair[1])
		}
	}
}

// protectedRepo is a workspace with every kind of protected path: a git
// directory with hooks and config, a core.hooksPath directory, agent settings
// and a protected directory that does not exist yet (.liveagent).
func protectedRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	files := map[string]string{
		".git/config":              "[core]\n\thooksPath = .githooks\n",
		".git/HEAD":                "ref: refs/heads/main\n",
		".git/hooks/pre-commit":    "#!/bin/sh\nexit 0\n",
		".git/objects/info/.keep":  "",
		".git/refs/heads/.keep":    "",
		".githooks/pre-push":       "#!/bin/sh\nexit 0\n",
		".k-brain/settings.json":   "{}\n",
		".k-brain/prompts/main.md": "be nice\n",
		"src/main.go":              "package main\n",
		"README.md":                "readme\n",
	}
	for name, content := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0o644)
		if strings.Contains(name, "hooks/") {
			mode = 0o755
		}
		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// protectedState records everything about the protected paths an attack could
// change: each entry's inode, type, mode, size, content and link target, and
// the inodes of the directories on the way.
func protectedState(t *testing.T, root string) string {
	t.Helper()
	var lines []string
	record := func(path string) {
		var st unix.Stat_t
		if err := unix.Lstat(path, &st); err != nil {
			lines = append(lines, fmt.Sprintf("%s: %v", path, err))
			return
		}
		line := fmt.Sprintf("%s: ino=%d mode=%o uid=%d gid=%d size=%d", path, st.Ino, st.Mode, st.Uid, st.Gid, st.Size)
		switch st.Mode & unix.S_IFMT {
		case unix.S_IFREG:
			data, _ := os.ReadFile(path)
			line += fmt.Sprintf(" data=%q", data)
		case unix.S_IFLNK:
			target, _ := os.Readlink(path)
			line += " link=" + target
		}
		lines = append(lines, line)
	}
	for _, dir := range []string{root, filepath.Join(root, ".git")} {
		record(dir)
	}
	for _, top := range []string{".git/hooks", ".git/config", ".githooks", ".k-brain", ".liveagent", ".git/config.worktree"} {
		_ = filepath.WalkDir(filepath.Join(root, top), func(path string, _ fs.DirEntry, err error) error {
			record(path)
			return nil
		})
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

func protectedPolicy(t *testing.T, root string) *Policy {
	t.Helper()
	p := landlockPolicy(t, root, true)
	want := []string{".git/hooks", ".git/config", ".githooks", ".k-brain", ".liveagent"}
	for _, name := range want {
		if !containsPath(p.Protected, filepath.Join(root, name)) {
			t.Fatalf("%s is not protected: %q", name, p.Protected)
		}
	}
	return p
}

func containsPath(paths []string, path string) bool {
	for _, p := range paths {
		if filepath.Clean(p) == path {
			return true
		}
	}
	return false
}

// TestLandlockProtectedPathsShellAttacks runs every shell-level way to change
// a protected path and checks after each that nothing changed.
func TestLandlockProtectedPathsShellAttacks(t *testing.T) {
	protectOnly(t)
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh")
	}
	attacks := []struct{ name, script string }{
		{"overwrite config", `echo evil > .git/config`},
		{"append config", `echo evil >> .git/config`},
		{"overwrite hook", `echo evil > .git/hooks/pre-commit`},
		{"create hook", `echo evil > .git/hooks/post-checkout && chmod +x .git/hooks/post-checkout`},
		{"create hooksPath hook", `echo evil > .githooks/post-checkout`},
		{"overwrite agent settings", `echo evil > .k-brain/settings.json`},
		{"create agent prompt", `echo evil > .k-brain/prompts/evil.md`},
		{"create missing .liveagent", `mkdir .liveagent && echo evil > .liveagent/settings.json`},
		{"create missing .liveagent as a file", `echo evil > .liveagent`},
		{"create missing .liveagent as a symlink", `ln -s src .liveagent`},
		{"create missing config.worktree", `echo evil > .git/config.worktree`},
		{"delete hook", `rm -f .git/hooks/pre-commit`},
		{"delete config", `rm -f .git/config`},
		{"delete hooks dir", `rm -rf .git/hooks`},
		{"delete agent dir", `rm -rf .k-brain`},
		{"delete git dir", `rm -rf .git`},
		{"rename config away", `mv .git/config .git/config.bak`},
		{"rename hooks away", `mv .git/hooks .git/hooks.bak`},
		{"rename hooksPath dir away", `mv .githooks githooks.bak`},
		{"rename agent dir away", `mv .k-brain kb && mkdir .k-brain && echo evil > .k-brain/settings.json`},
		{"rename git dir and recreate", `mv .git .git2 && mkdir -p .git/hooks && echo evil > .git/hooks/pre-commit`},
		{"rename root entry over config", `echo evil > evil && mv -f evil .git/config`},
		{"rename root entry into hooks", `echo evil > evil && mv evil .git/hooks/post-checkout`},
		{"rename src dir over agent dir", `mkdir -p d && mv -T d .k-brain`},
		{"rename src dir to .liveagent", `mkdir -p d && mv d .liveagent`},
		{"hardlink config out", `ln .git/config cfg && echo evil >> cfg`},
		{"hardlink config into src", `ln .git/config src/cfg && echo evil >> src/cfg`},
		{"hardlink into hooks", `echo evil > evil && ln evil .git/hooks/post-checkout`},
		{"truncate config", `truncate -s 0 .git/config`},
		{"truncate by redirection", `: > .git/config`},
		{"truncate hook", `truncate -s 0 .git/hooks/pre-commit`},
		{"symlink to hooks then write", `ln -s .git/hooks h && echo evil > h/pre-commit`},
		{"symlink to config then write", `ln -s .git/config c && echo evil > c`},
		{"symlink to git dir then create", `ln -s .git g && echo evil > g/hooks/post-checkout`},
		{"replace hooks with a symlink", `mkdir -p evilhooks && echo evil > evilhooks/pre-commit && rm -rf .git/hooks; ln -sfn ../evilhooks .git/hooks`},
		{"replace config with a symlink", `echo evil > evilcfg && ln -sf ../evilcfg .git/config`},
		{"chmod hook", `chmod 777 .git/hooks/pre-commit`},
		{"chmod hooks dir", `chmod 777 .git/hooks`},
		{"chmod agent dir", `chmod 000 .k-brain`},
		{"chmod config via symlink", `ln -s .git/config c2; chmod 666 c2`},
		{"chgrp hook", "chgrp $(id -g) .git/hooks/pre-commit"},
		{"chgrp hooks recursively", "chgrp -R $(id -g) .git/hooks"},
		{"mkfifo in hooks", `mkfifo .git/hooks/post-checkout`},
		{"sed -i config", `sed -i s/core/evil/ .git/config`},
		{"cp over config", `echo evil > evil && cp evil .git/config`},
		{"tar over agent dir", `mkdir -p x/.k-brain && echo evil > x/.k-brain/settings.json && tar -C x -cf x.tar .k-brain && tar -xf x.tar`},
		{"proc cwd path", `echo evil > /proc/self/cwd/.git/hooks/post-checkout`},
		{"absolute path", `echo evil > "$PWD/.git/hooks/post-checkout"`},
		{"dot-dot path", `mkdir -p src/a && cd src/a && echo evil > ../../.git/hooks/post-checkout`},
	}
	for _, attack := range attacks {
		t.Run(attack.name, func(t *testing.T) {
			root := protectedRepo(t)
			before := protectedState(t, root)
			out, err := landlockRun(t, protectedPolicy(t, root), "/bin/sh", "-c", attack.script)
			after := protectedState(t, root)
			if before != after {
				t.Fatalf("protected paths changed (command err %v, output %q):\nbefore:\n%s\nafter:\n%s", err, out, before, after)
			}
			if err == nil {
				t.Fatalf("attack exited 0 without changing anything; check the script: %q", out)
			}
			t.Logf("refused: %v %s", err, strings.TrimSpace(out))
		})
	}
}

// TestLandlockProtectedPathsNormalEdits checks that the usual edits keep
// working beside the protected paths, at the workspace root and in new
// directories.
func TestLandlockProtectedPathsNormalEdits(t *testing.T) {
	protectOnly(t)
	root := protectedRepo(t)
	p := protectedPolicy(t, root)
	before := protectedState(t, root)
	script := `set -e
echo new > root.txt
echo more >> root.txt
echo edit >> README.md
sed -i s/readme/README/ README.md
mv root.txt renamed.txt
cp renamed.txt copy.txt
ln copy.txt hard.txt
ln -s renamed.txt link.txt
cat link.txt > /dev/null
rm copy.txt
: > hard.txt
truncate -s 3 renamed.txt
mkdir -p newdir/a/b
echo deep > newdir/a/b/f.txt
mv newdir/a/b/f.txt newdir/a/g.txt
mv newdir src/newdir
mv src/main.go main.go && mv main.go src/main.go
mkdir build && echo o > build/out && rm -r build
echo '#!/bin/sh' > run.sh && chmod +x run.sh && ./run.sh
chmod 600 README.md
chgrp "$(id -g)" README.md
t=$(mktemp) && echo tmp > "$t" && rm "$t"
d=$(mktemp -d) && echo x > "$d/f" && rm -r "$d"
echo lock > .git/index.lock && mv .git/index.lock .git/index
echo ref > .git/refs/heads/main.lock && mv .git/refs/heads/main.lock .git/refs/heads/main
echo obj > .git/objects/info/packs
echo msg > .git/COMMIT_EDITMSG
mkdir .git/logs && echo log > .git/logs/HEAD
cat .git/hooks/pre-commit .git/config .k-brain/settings.json > /dev/null
echo done`
	out, err := landlockRun(t, p, "/bin/sh", "-c", script)
	if err != nil || !strings.Contains(out, "done") {
		t.Fatalf("normal edits failed: %v\n%s", err, out)
	}
	checks := map[string]string{
		"README.md":            "README\nedit\n",
		"renamed.txt":          "new",
		"hard.txt":             "",
		"src/newdir/a/g.txt":   "deep\n",
		"src/main.go":          "package main\n",
		".git/index":           "lock\n",
		".git/refs/heads/main": "ref\n",
		".git/logs/HEAD":       "log\n",
	}
	for name, want := range checks {
		if got, err := os.ReadFile(filepath.Join(root, name)); err != nil || string(got) != want {
			t.Errorf("%s = %q (%v), want %q", name, got, err, want)
		}
	}
	if info, err := os.Stat(filepath.Join(root, "run.sh")); err != nil || info.Mode().Perm()&0o100 == 0 {
		t.Errorf("run.sh is not executable: %v %v", info, err)
	}
	if info, err := os.Stat(filepath.Join(root, "README.md")); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("README.md mode not changed: %v %v", info, err)
	}
	for _, gone := range []string{"copy.txt", "build", "newdir"} {
		if _, err := os.Lstat(filepath.Join(root, gone)); err == nil {
			t.Errorf("%s still exists", gone)
		}
	}
	if after := protectedState(t, root); after != before {
		t.Fatalf("normal edits changed protected paths:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// TestLandlockProtectedPathsGit runs git in a repository under the sandbox:
// add and commit work, changing the configuration does not.
func TestLandlockProtectedPathsGit(t *testing.T) {
	protectOnly(t)
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	setup := exec.Command(git, "init", "-q", "-b", "main", root)
	if out, err := setup.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := landlockPolicy(t, root, true)
	before := protectedState(t, root)
	env := "GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@example.com GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@example.com"
	out, err := landlockRun(t, p, "/bin/sh", "-c", `set -e
export `+env+`
git add a.txt
git commit -q -m first
echo b > b.txt
git add b.txt
git commit -q -m second
git checkout -q -b topic
git log --oneline | wc -l`)
	if err != nil || strings.TrimSpace(out) != "2" {
		t.Fatalf("git add/commit failed under the sandbox: %v\n%s", err, out)
	}
	if out, err := landlockRun(t, p, "/bin/sh", "-c", `git config core.hooksPath /tmp/evil`); err == nil {
		t.Fatalf("git config changed the protected config: %s", out)
	}
	if out, err := landlockRun(t, p, "/bin/sh", "-c", `printf '#!/bin/sh\necho evil\n' > .git/hooks/pre-commit`); err == nil {
		t.Fatalf("hook written: %s", out)
	}
	if after := protectedState(t, root); after != before {
		t.Fatalf("git changed protected paths:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// TestLandlockProtectedPathsSyscallAttacks re-runs the test binary inside the
// launcher for the attacks a shell cannot express.
func TestLandlockProtectedPathsSyscallAttacks(t *testing.T) {
	protectOnly(t)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := protectedRepo(t)
	before := protectedState(t, root)
	p := protectedPolicy(t, root)
	cmd := exec.Command(self, "-test.run=^TestProtectAttackHelper$", "-test.v")
	cmd.Dir = root
	wrapped, err := p.Wrap(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	wrapped.Env = append(os.Environ(), "KBRAIN_PROTECT_HELPER=1")
	out, err := wrapped.CombinedOutput()
	t.Logf("helper:\n%s", out)
	if err != nil || !strings.Contains(string(out), "helper ok") {
		t.Fatalf("helper failed: %v", err)
	}
	if after := protectedState(t, root); after != before {
		t.Fatalf("protected paths changed:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	for _, leftover := range []string{"race-ok-file", "rename-src"} {
		_ = os.Remove(filepath.Join(root, leftover))
	}
}

func TestProtectAttackHelper(t *testing.T) {
	if os.Getenv("KBRAIN_PROTECT_HELPER") != "1" {
		t.Skip("runs only inside TestLandlockProtectedPathsSyscallAttacks")
	}
	refused := func(name string, err error) {
		t.Helper()
		if err == nil {
			t.Errorf("%s succeeded", name)
			return
		}
		fmt.Printf("refused %-44s %v\n", name+":", err)
	}
	openFile := func(path string, flags int) error {
		fd, err := unix.Open(path, flags|unix.O_CLOEXEC, 0o644)
		if err == nil {
			unix.Close(fd)
		}
		return err
	}
	refused("O_RDONLY|O_TRUNC on config", openFile(".git/config", unix.O_RDONLY|unix.O_TRUNC))
	refused("O_WRONLY on config", openFile(".git/config", unix.O_WRONLY))
	refused("O_RDWR|O_APPEND on hook", openFile(".git/hooks/pre-commit", unix.O_RDWR|unix.O_APPEND))
	refused("O_CREAT|O_EXCL in hooks", openFile(".git/hooks/post-merge", unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL))
	refused("O_CREAT .liveagent", openFile(".liveagent", unix.O_WRONLY|unix.O_CREAT))

	how := &unix.OpenHow{Flags: unix.O_WRONLY | unix.O_CREAT, Mode: 0o644}
	_, err := unix.Openat2(unix.AT_FDCWD, ".git/hooks/post-merge", how)
	refused("openat2 create in hooks", err)
	how.Resolve = unix.RESOLVE_BENEATH
	_, err = unix.Openat2(unix.AT_FDCWD, ".git/hooks/post-merge", how)
	refused("openat2 RESOLVE_BENEATH create in hooks", err)

	// A descriptor opened for reading cannot be reopened for writing.
	rd, err := unix.Open(".git/config", unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	refused("reopen read fd via /proc/self/fd for writing", openFile(fmt.Sprintf("/proc/self/fd/%d", rd), unix.O_WRONLY))
	refused("reopen read fd via /proc/thread-self/fd", openFile(fmt.Sprintf("/proc/thread-self/fd/%d", rd), unix.O_RDWR))
	refused("fchmod read fd of config", unix.Fchmod(rd, 0o666))
	refused("chmod /proc/self/fd/N of config", unix.Chmod(fmt.Sprintf("/proc/self/fd/%d", rd), 0o666))
	refused("chmod /dev/fd/N of config", unix.Chmod(fmt.Sprintf("/dev/fd/%d", rd), 0o666))
	refused("ftruncate read fd", unix.Ftruncate(rd, 0))
	_, err = unix.Write(rd, []byte("evil"))
	refused("write to read fd", err)
	unix.Close(rd)

	opath, err := unix.Open(".git/hooks/pre-commit", unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	refused("fchmodat AT_EMPTY_PATH on O_PATH hook", fchmodat2(opath, "", 0o777, unix.AT_EMPTY_PATH))
	refused("fchmodat via /proc/self/fd of O_PATH hook", unix.Fchmodat(unix.AT_FDCWD, fmt.Sprintf("/proc/self/fd/%d", opath), 0o777, 0))
	unix.Close(opath)
	hooks, err := unix.Open(".git/hooks", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	refused("fchmod hooks dir fd", unix.Fchmod(hooks, 0o777))
	refused("fchmodat relative to hooks fd", unix.Fchmodat(hooks, "pre-commit", 0o777, 0))
	refused("openat relative to hooks fd", openFile(fmt.Sprintf("/proc/self/fd/%d/pre-commit", hooks), unix.O_WRONLY))
	fd, err := unix.Openat(hooks, "post-merge", unix.O_WRONLY|unix.O_CREAT|unix.O_CLOEXEC, 0o755)
	if err == nil {
		unix.Close(fd)
	}
	refused("openat create relative to hooks fd", err)
	refused("mkdirat in hooks fd", unix.Mkdirat(hooks, "sub", 0o755))
	refused("symlinkat in hooks fd", unix.Symlinkat("/bin/true", hooks, "post-merge"))
	refused("unlinkat in hooks fd", unix.Unlinkat(hooks, "pre-commit", 0))
	refused("mknodat fifo in hooks fd", unix.Mknodat(hooks, "post-merge", unix.S_IFIFO|0o644, 0))
	unix.Close(hooks)

	refused("chmod hook", unix.Chmod(".git/hooks/pre-commit", 0o777))
	refused("chmod hooksPath hook", unix.Chmod(".githooks/pre-push", 0o777))
	refused("chmod agent prompts dir", unix.Chmod(".k-brain/prompts", 0o777))
	gid := os.Getgid()
	refused("chown keeping the ids on hook", unix.Chown(".git/hooks/pre-commit", -1, gid))
	refused("lchown on agent settings", unix.Lchown(".k-brain/settings.json", -1, gid))
	refused("fchownat on hooks dir", unix.Fchownat(unix.AT_FDCWD, ".git/hooks", -1, gid, 0))
	if cfg, err := unix.Open(".git/config", unix.O_RDONLY|unix.O_CLOEXEC, 0); err == nil {
		refused("fchown read fd of config", unix.Fchown(cfg, -1, gid))
		unix.Close(cfg)
	}
	if err := os.WriteFile("owned", nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := unix.Chown("owned", -1, gid); err != nil {
		t.Errorf("chown of a new root file: %v", err)
	}
	refused("truncate(2) config", unix.Truncate(".git/config", 0))
	refused("truncate(2) agent settings", unix.Truncate(".k-brain/settings.json", 0))
	refused("setxattr ACL on hook", unix.Setxattr(".git/hooks/pre-commit", "system.posix_acl_access", []byte{2, 0, 0, 0}, 0))
	refused("setxattr user attr on config", unix.Setxattr(".git/config", "user.evil", []byte("x"), 0))

	if err := os.WriteFile("evil", []byte("evil\n"), 0o755); err != nil {
		t.Fatalf("root-level create failed: %v", err)
	}
	refused("RENAME_EXCHANGE root file with config", unix.Renameat2(unix.AT_FDCWD, "evil", unix.AT_FDCWD, ".git/config", unix.RENAME_EXCHANGE))
	refused("RENAME_EXCHANGE config with root file", unix.Renameat2(unix.AT_FDCWD, ".git/config", unix.AT_FDCWD, "evil", unix.RENAME_EXCHANGE))
	refused("RENAME_EXCHANGE .git with dir", func() error {
		if err := os.Mkdir("fakegit", 0o755); err != nil {
			return nil
		}
		return unix.Renameat2(unix.AT_FDCWD, "fakegit", unix.AT_FDCWD, ".git", unix.RENAME_EXCHANGE)
	}())
	refused("RENAME_EXCHANGE src with hooks", unix.Renameat2(unix.AT_FDCWD, "src", unix.AT_FDCWD, ".git/hooks", unix.RENAME_EXCHANGE))
	refused("RENAME_NOREPLACE into hooks", unix.Renameat2(unix.AT_FDCWD, "evil", unix.AT_FDCWD, ".git/hooks/post-merge", unix.RENAME_NOREPLACE))
	refused("RENAME_WHITEOUT over config", unix.Renameat2(unix.AT_FDCWD, "evil", unix.AT_FDCWD, ".git/config", unix.RENAME_WHITEOUT))
	refused("rename .git", unix.Rename(".git", ".git2"))
	refused("rename root over .git", unix.Rename("fakegit", ".git"))
	refused("link hook out", unix.Link(".git/hooks/pre-commit", "hooklink"))
	refused("link into hooksPath", unix.Link("evil", ".githooks/post-merge"))
	refused("linkat following a symlink to config", func() error {
		if err := unix.Symlink(".git/config", "cfglink"); err != nil {
			return err
		}
		return unix.Linkat(unix.AT_FDCWD, "cfglink", unix.AT_FDCWD, "cfghard", unix.AT_SYMLINK_FOLLOW)
	}())

	// An unnamed file made at the root cannot be linked into the hooks.
	tmp, err := unix.Open(".", unix.O_TMPFILE|unix.O_WRONLY|unix.O_CLOEXEC, 0o755)
	if err != nil {
		t.Fatalf("O_TMPFILE at the root: %v", err)
	}
	if _, err := unix.Write(tmp, []byte("#!/bin/sh\necho evil\n")); err != nil {
		t.Fatal(err)
	}
	refused("O_TMPFILE linked into hooks", unix.Linkat(unix.AT_FDCWD, fmt.Sprintf("/proc/self/fd/%d", tmp), unix.AT_FDCWD, ".git/hooks/post-merge", unix.AT_SYMLINK_FOLLOW))
	refused("O_TMPFILE linked as .liveagent", unix.Linkat(unix.AT_FDCWD, fmt.Sprintf("/proc/self/fd/%d", tmp), unix.AT_FDCWD, ".liveagent", unix.AT_SYMLINK_FOLLOW))
	refused("O_TMPFILE AT_EMPTY_PATH into hooks", unix.Linkat(tmp, "", unix.AT_FDCWD, ".git/hooks/post-merge", unix.AT_EMPTY_PATH))
	if err := unix.Linkat(unix.AT_FDCWD, fmt.Sprintf("/proc/self/fd/%d", tmp), unix.AT_FDCWD, "tmpfile-named", unix.AT_SYMLINK_FOLLOW); err != nil {
		t.Errorf("O_TMPFILE linked at the root: %v", err)
	}
	unix.Close(tmp)
	tmpHooks, err := unix.Open(".git/hooks", unix.O_TMPFILE|unix.O_WRONLY|unix.O_CLOEXEC, 0o755)
	if err == nil {
		unix.Close(tmpHooks)
	}
	refused("O_TMPFILE in hooks", err)

	raceOpen(t)
	raceRename(t)
	fmt.Println("helper ok")
}

func fchmodat2(dirfd int, path string, mode uint32, flags int) error {
	p, err := unix.BytePtrFromString(path)
	if err != nil {
		return err
	}
	_, _, errno := unix.Syscall6(unix.SYS_FCHMODAT2, uintptr(dirfd), uintptr(unsafe.Pointer(p)), uintptr(mode), uintptr(flags), 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

// flipper rewrites a path buffer between a harmless name and a protected
// one while another thread passes it to the kernel, to catch a supervisor
// that reads the path once and acts on a different one.
type flipper struct {
	buf  []uint64
	stop atomic.Bool
}

func newFlipper(harmless, evil string) (*flipper, func()) {
	if len(harmless) != len(evil) || len(harmless)%8 != 0 {
		panic("flipper paths must have the same length, a multiple of 8")
	}
	words := func(s string) []uint64 {
		b := []byte(s)
		out := make([]uint64, len(b)/8)
		for i := range out {
			out[i] = *(*uint64)(unsafe.Pointer(&b[i*8]))
		}
		return out
	}
	a, e := words(harmless), words(evil)
	f := &flipper{buf: make([]uint64, len(a)+1)}
	copy(f.buf, a)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; !f.stop.Load(); i++ {
			src := a
			if i%2 == 1 {
				src = e
			}
			for j, w := range src {
				atomic.StoreUint64(&f.buf[j], w)
			}
		}
	}()
	return f, func() { f.stop.Store(true); <-done }
}

// atFDCWD is AT_FDCWD as a variable, so it converts to a uintptr.
var atFDCWD = unix.AT_FDCWD

func (f *flipper) ptr() uintptr { return uintptr(unsafe.Pointer(&f.buf[0])) }

func raceOpen(t *testing.T) {
	// Both 16 bytes: a root-level file and a new hook.
	f, stop := newFlipper("race-ok-file-000", ".git/hooks/racer")
	deadline := time.Now().Add(2 * time.Second)
	ok := 0
	for n := 0; time.Now().Before(deadline); n++ {
		fd, _, errno := unix.Syscall6(unix.SYS_OPENAT, uintptr(atFDCWD), f.ptr(), uintptr(unix.O_WRONLY|unix.O_CREAT|unix.O_CLOEXEC), 0o755, 0, 0)
		if errno == 0 {
			ok++
			unix.Close(int(fd))
		}
		if _, err := os.Lstat(".git/hooks/racer"); err == nil {
			stop()
			t.Fatal("the racing open created .git/hooks/racer")
		}
	}
	stop()
	runtime.KeepAlive(f)
	fmt.Printf("race open: %d opens of the harmless name succeeded, none reached the hooks\n", ok)
	if ok == 0 {
		t.Error("the racing open never succeeded on the harmless name; the race did not run")
	}
}

func raceRename(t *testing.T) {
	f, stop := newFlipper("rename-dst-00000", ".git/hooks/racer")
	deadline := time.Now().Add(2 * time.Second)
	src, _ := unix.BytePtrFromString("rename-src")
	moved := 0
	for time.Now().Before(deadline) {
		if err := os.WriteFile("rename-src", []byte("evil"), 0o755); err != nil {
			stop()
			t.Fatalf("recreate the rename source: %v", err)
		}
		_, _, errno := unix.Syscall6(unix.SYS_RENAMEAT2, uintptr(atFDCWD), uintptr(unsafe.Pointer(src)), uintptr(atFDCWD), f.ptr(), 0, 0)
		if errno == 0 {
			moved++
		}
		if _, err := os.Lstat(".git/hooks/racer"); err == nil {
			stop()
			t.Fatal("the racing rename reached .git/hooks/racer")
		}
	}
	stop()
	fmt.Printf("race rename: %d renames to the harmless name succeeded, none reached the hooks\n", moved)
	if moved == 0 {
		t.Error("the racing rename never succeeded on the harmless name; the race did not run")
	}
	_ = os.Remove("rename-dst-00000")
}

// TestLandlockSupervisorExit checks that the launcher reports the command's
// exit status and signal like a direct run.
func TestLandlockSupervisorExit(t *testing.T) {
	protectOnly(t)
	root := protectedRepo(t)
	p := protectedPolicy(t, root)
	_, err := landlockRun(t, p, "/bin/sh", "-c", "exit 7")
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 7 {
		t.Fatalf("exit 7 reported as %v", err)
	}
	_, err = landlockRun(t, p, "/bin/sh", "-c", "kill -TERM $$")
	if !errors.As(err, &exit) {
		t.Fatalf("signal reported as %v", err)
	}
	if status, ok := exit.Sys().(syscall.WaitStatus); !ok || !status.Signaled() || status.Signal() != syscall.SIGTERM {
		t.Fatalf("SIGTERM reported as %v", err)
	}
}

// TestLandlockSpecProtect checks that the spec lists the protected paths, and
// that a policy without any keeps the single-process launcher.
func TestLandlockSpecProtect(t *testing.T) {
	protectOnly(t)
	root := protectedRepo(t)
	spec, err := protectedPolicy(t, root).landlockSpec(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".git/hooks", ".git/config", ".githooks", ".k-brain", ".liveagent"} {
		if !containsPath(spec.Protect, filepath.Join(root, name)) {
			t.Errorf("spec does not protect %s: %q", name, spec.Protect)
		}
	}
	plain := &Policy{Mode: "workspace", Backend: mechanismLandlock, Root: root, Network: true}
	if spec, err := plain.landlockSpec(root); err != nil || len(spec.Protect) != 0 {
		t.Fatalf("policy without protected paths: %v %q", err, spec.Protect)
	}
}
