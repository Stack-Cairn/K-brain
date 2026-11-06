//go:build linux

package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Protected paths under Landlock.
//
// Landlock rights are inherited down the tree and can only be granted, so a
// rule that lets a command create files in the workspace root also lets it
// create them in .git/hooks. With protected paths the launcher therefore runs
// in two processes:
//
//   - the command's Landlock domain has no rule on the directories between a
//     write grant and a protected path ("locked" directories: the workspace
//     root, .git, ...) nor on the protected paths, but a full rule on every
//     other entry of the locked directories. Everything below src/ or
//     node_modules/ stays writable as before, while the kernel refuses every
//     change to a locked directory's entries and to the protected trees;
//   - a seccomp filter hands the calls that create, remove, rename, link, open
//     for writing, truncate or chmod a path to a supervisor (the launcher's
//     first process) through SECCOMP_RET_USER_NOTIF. The supervisor performs
//     the call itself, on the command's behalf, when it changes an entry of a
//     locked directory, or of a directory created in one during the run, that
//     is neither a protected path nor on the way to one. Everything else
//     continues in the kernel under the command's Landlock domain.
//
// Continuing a call is always safe: the kernel then checks it against the
// command's domain, so a command that changes the path in its memory after the
// supervisor read it gains nothing. The supervisor only decides on its own
// copy of the arguments, resolves the parent directory to a file descriptor
// and acts on that descriptor and a single name, so the inode it checked is
// the inode it changes. Protected paths and locked directories cannot be
// removed, renamed or replaced by either side during the run, which keeps the
// inode sets below valid. A mode change is not a Landlock right, so chmod is
// never continued: the supervisor performs or refuses it.

const (
	// launcherChildArg0 is argv[0] of the second stage: the process that
	// confines itself and execs the command under the supervisor.
	launcherChildArg0 = "k-brain-sandbox-child"
	// minProtectABI is the Landlock ABI protected paths need: ABI 3 handles
	// truncation, which earlier ABIs leave allowed everywhere.
	minProtectABI = 3
	// frozenLimit bounds the walk that lists the inodes beneath the protected
	// paths (used to refuse chmod on them).
	frozenLimit = 50000
)

// childSpec is the second stage's configuration.
type childSpec struct {
	Network bool `json:"network,omitempty"`
	// Parent is the supervisor's pid, so a child whose supervisor already
	// died does not run the command unsupervised.
	Parent int `json:"parent"`
}

// fileKey identifies an inode.
type fileKey struct{ dev, ino uint64 }

func keyOf(st *unix.Stat_t) fileKey { return fileKey{dev: uint64(st.Dev), ino: st.Ino} }

// protectPlan is the protected-path layout of one launch.
type protectPlan struct {
	// locked are the directories from a write grant down to a protected path.
	locked map[fileKey]bool
	// protected are the existing protected paths.
	protected map[fileKey]bool
	// frozen are the protected paths and everything beneath them; frozenAll
	// is false when the walk stopped at frozenLimit.
	frozen    map[fileKey]bool
	frozenAll bool
	// rules are the directories with a write rule in the command's domain.
	rules map[fileKey]bool
	// reserved are names in locked directories under which a protected path
	// that does not exist yet would appear; they cannot be created.
	reserved map[fileKey]map[string]bool
}

// planProtection works out the locked directories for the protected paths
// inside the grants and builds the command's Landlock ruleset: a write rule on
// every grant that holds no protected path and on every entry of a locked
// directory that is neither locked nor protected. It returns the ruleset's
// file descriptor.
func planProtection(abi int, network bool, grants, protect []string) (*protectPlan, int, error) {
	plan := &protectPlan{locked: map[fileKey]bool{}, protected: map[fileKey]bool{}, frozen: map[fileKey]bool{}, frozenAll: true,
		rules: map[fileKey]bool{}, reserved: map[fileKey]map[string]bool{}}
	// Landlock follows the real directory tree, so the grants are compared
	// with the protected paths (already resolved) without symlinks.
	canonical := make([]string, 0, len(grants))
	for _, grant := range grants {
		if resolved, err := filepath.EvalSymlinks(grant); err == nil {
			grant = resolved
		}
		canonical = append(canonical, filepath.Clean(grant))
	}
	grants = canonical
	isProtected := map[string]bool{}
	for _, path := range protect {
		isProtected[filepath.Clean(path)] = true
	}
	lockedPaths := map[string]bool{}
	var lockedOrder, protectedPaths []string
	reservedPaths := map[string][]string{}
	lock := func(dir string) {
		if !lockedPaths[dir] {
			lockedPaths[dir] = true
			lockedOrder = append(lockedOrder, dir)
		}
	}
	for _, path := range protect {
		path = filepath.Clean(path)
		for _, grant := range grants {
			grant = filepath.Clean(grant)
			if grant == path || !encloses(grant, path) {
				continue
			}
			info, err := os.Lstat(grant)
			if err != nil || !info.IsDir() {
				continue
			}
			lock(grant)
			rel, err := filepath.Rel(grant, path)
			if err != nil {
				return nil, -1, err
			}
			current := grant
			for _, part := range strings.Split(rel, string(filepath.Separator)) {
				next := filepath.Join(current, part)
				info, err := os.Lstat(next)
				if errors.Is(err, fs.ErrNotExist) {
					reservedPaths[current] = appendNew(reservedPaths[current], part)
					break
				}
				if err != nil {
					return nil, -1, fmt.Errorf("protected path %s: %w", next, err)
				}
				// A protected path, or a file or link where a directory on
				// the way would be, is kept as it is.
				if isProtected[next] || !info.IsDir() {
					protectedPaths = appendNew(protectedPaths, next)
					break
				}
				lock(next)
				current = next
			}
		}
	}
	for _, dir := range lockedOrder {
		st, err := lstatKey(dir)
		if err != nil {
			return nil, -1, err
		}
		plan.locked[st] = true
	}
	for dir, names := range reservedPaths {
		key, err := lstatKey(dir)
		if err != nil {
			return nil, -1, err
		}
		set := map[string]bool{}
		for _, name := range names {
			set[name] = true
		}
		plan.reserved[key] = set
	}
	seen := 0
	for _, path := range protectedPaths {
		key, err := lstatKey(path)
		if err != nil {
			return nil, -1, err
		}
		plan.protected[key] = true
		_ = filepath.WalkDir(path, func(file string, entry fs.DirEntry, err error) error {
			if err != nil {
				plan.frozenAll = false
				return nil
			}
			if seen++; seen > frozenLimit {
				plan.frozenAll = false
				return filepath.SkipAll
			}
			if key, err := lstatKey(file); err == nil {
				plan.frozen[key] = true
			} else {
				plan.frozenAll = false
			}
			return nil
		})
	}

	ruleset, access, err := createRuleset(abi, network)
	if err != nil {
		return nil, -1, err
	}
	fail := func(err error) (*protectPlan, int, error) {
		unix.Close(ruleset)
		return nil, -1, err
	}
	for _, grant := range grants {
		if lockedPaths[filepath.Clean(grant)] {
			continue
		}
		key, dir, err := addRule(ruleset, grant, access, false)
		if err != nil {
			return fail(err)
		}
		if dir {
			plan.rules[key] = true
		}
	}
	for _, dir := range lockedOrder {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return fail(fmt.Errorf("list %s: %w", dir, err))
		}
		for _, entry := range entries {
			path := filepath.Join(dir, entry.Name())
			if lockedPaths[path] || isProtected[path] || entry.Type()&fs.ModeSymlink != 0 {
				continue
			}
			// A second name of a protected file gets no rule either.
			if key, err := lstatKey(path); err != nil || plan.frozen[key] {
				continue
			}
			key, isDir, err := addRule(ruleset, path, access, true)
			if err != nil {
				return fail(err)
			}
			if isDir {
				plan.rules[key] = true
			}
		}
	}
	return plan, ruleset, nil
}

func lstatKey(path string) (fileKey, error) {
	var st unix.Stat_t
	if err := unix.Lstat(path, &st); err != nil {
		return fileKey{}, fmt.Errorf("stat %s: %w", path, err)
	}
	return keyOf(&st), nil
}

// createRuleset creates a Landlock ruleset handling every write right of the
// ABI and, for an offline run, TCP and the scopes (see restrictWrites).
func createRuleset(abi int, network bool) (int, uint64, error) {
	access := landlockWriteAccess(abi)
	attr := unix.LandlockRulesetAttr{Access_fs: access}
	if !network && abi >= 4 {
		attr.Access_net = unix.LANDLOCK_ACCESS_NET_BIND_TCP | unix.LANDLOCK_ACCESS_NET_CONNECT_TCP
	}
	if abi >= 6 {
		attr.Scoped = unix.LANDLOCK_SCOPE_SIGNAL
		if !network {
			attr.Scoped |= unix.LANDLOCK_SCOPE_ABSTRACT_UNIX_SOCKET
		}
	}
	fd, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, uintptr(unsafe.Pointer(&attr)), landlockRulesetAttrSize(abi), 0)
	if errno != 0 {
		return -1, 0, fmt.Errorf("create Landlock ruleset (ABI %d): %w", abi, errno)
	}
	return int(fd), access, nil
}

// addRule grants writes beneath path and reports the inode it was added for.
// With noFollow a symlink is skipped instead of followed. A missing path is
// skipped.
func addRule(ruleset int, path string, access uint64, noFollow bool) (fileKey, bool, error) {
	flags := unix.O_PATH | unix.O_CLOEXEC
	if noFollow {
		flags |= unix.O_NOFOLLOW
	}
	fd, err := unix.Open(path, flags, 0)
	if errors.Is(err, unix.ENOENT) {
		return fileKey{}, false, nil
	}
	if err != nil {
		return fileKey{}, false, fmt.Errorf("open %s for a Landlock rule: %w", path, err)
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return fileKey{}, false, fmt.Errorf("stat %s: %w", path, err)
	}
	mode := st.Mode & unix.S_IFMT
	if mode == unix.S_IFLNK {
		return fileKey{}, false, nil
	}
	if mode != unix.S_IFDIR {
		access &= landlockFileAccess
	}
	rule := unix.LandlockPathBeneathAttr{Allowed_access: access, Parent_fd: int32(fd)}
	if _, _, errno := unix.Syscall6(unix.SYS_LANDLOCK_ADD_RULE, uintptr(ruleset), unix.LANDLOCK_RULE_PATH_BENEATH, uintptr(unsafe.Pointer(&rule)), 0, 0, 0); errno != 0 {
		return fileKey{}, false, fmt.Errorf("add Landlock rule for %s: %w", path, errno)
	}
	return keyOf(&st), mode == unix.S_IFDIR, nil
}

// runSupervisor is the first stage with protected paths. It confines its own
// thread to the write grants (so a mistake here cannot write outside them),
// starts the second stage with the command's ruleset, receives the seccomp
// listener and serves it until the command exits, then exits the same way.
func runSupervisor(spec launchSpec, program string, argv []string) {
	abi := landlockABI()
	if abi < minProtectABI {
		launcherFail(fmt.Errorf("protected paths need Landlock ABI %d (Linux 6.2), this kernel has ABI %d", minProtectABI, abi))
	}
	arch, ok := seccompArches[runtime.GOARCH]
	if !ok {
		launcherFail(fmt.Errorf("seccomp filtering is not supported on %s", runtime.GOARCH))
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		launcherFail(fmt.Errorf("set no_new_privs: %w", err))
	}
	plan, ruleset, err := planProtection(abi, spec.Network, spec.Write, spec.Protect)
	if err != nil {
		launcherFail(err)
	}
	// The supervisor writes for the command, so it gets the grants the
	// launcher always had, and nothing else.
	if err := restrictWrites(abi, launchSpec{Write: spec.Write, Network: true}); err != nil {
		launcherFail(err)
	}
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		launcherFail(fmt.Errorf("socketpair: %w", err))
	}
	encoded, _ := json.Marshal(childSpec{Network: spec.Network, Parent: os.Getpid()})
	rulesetFile := os.NewFile(uintptr(ruleset), "landlock-ruleset")
	childSock := os.NewFile(uintptr(pair[1]), "supervisor-socket")
	cmd := &exec.Cmd{
		Path:       selfExe,
		Args:       append([]string{launcherChildArg0, string(encoded), program}, argv...),
		Env:        os.Environ(),
		Stdin:      os.Stdin,
		Stdout:     os.Stdout,
		Stderr:     os.Stderr,
		ExtraFiles: []*os.File{rulesetFile, childSock},
	}
	// Started from this locked thread, so the child inherits its Landlock
	// domain and its parent-death signal follows this thread.
	if err := cmd.Start(); err != nil {
		launcherFail(fmt.Errorf("start the sandboxed command: %w", err))
	}
	rulesetFile.Close()
	childSock.Close()
	listener, err := receiveFD(pair[0])
	unix.Close(pair[0])
	if err != nil {
		// The child failed before confining the command and said why.
		exitLike(cmd.Wait())
	}
	// The supervisor applies the command's umask itself.
	unix.Umask(0)
	// The terminal signals the whole process group; the supervisor must
	// outlive the command's own handling of them.
	signal.Ignore(syscall.SIGINT, syscall.SIGQUIT, syscall.SIGTSTP, syscall.SIGTTIN, syscall.SIGTTOU, syscall.SIGPIPE)
	forward := make(chan os.Signal, 4)
	signal.Notify(forward, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGUSR1, syscall.SIGUSR2)
	go func() {
		for sig := range forward {
			_ = cmd.Process.Signal(sig)
		}
	}()
	go func() { exitLike(cmd.Wait()) }()
	// A signal that interrupts SECCOMP_IOCTL_NOTIF_ADDFD after the kernel
	// queued a descriptor with SECCOMP_ADDFD_FLAG_SEND can leave the call
	// answered with 0 instead of the descriptor. The serving thread therefore
	// blocks the asynchronous signals (Go's preemption signal among them);
	// the process's signals go to its other threads.
	var async unix.Sigset_t
	for i := range async.Val {
		async.Val[i] = ^uint64(0)
	}
	for _, sig := range []unix.Signal{unix.SIGSEGV, unix.SIGBUS, unix.SIGFPE, unix.SIGILL, unix.SIGTRAP, unix.SIGSYS} {
		async.Val[(sig-1)/64] &^= 1 << ((uint(sig) - 1) % 64)
	}
	if err := unix.PthreadSigmask(unix.SIG_BLOCK, &async, nil); err != nil {
		_ = cmd.Process.Kill()
		launcherFail(fmt.Errorf("block signals on the supervisor thread: %w", err))
	}
	s := &supervisor{plan: plan, listener: listener, calls: arch.fs}
	err = s.serve()
	_ = cmd.Process.Kill()
	launcherFail(fmt.Errorf("supervisor: %w", err))
}

// exitLike exits with the command's status; a command killed by a signal
// gives 128 plus the signal number, as a shell reports it.
func exitLike(err error) {
	var exit *exec.ExitError
	if err == nil {
		os.Exit(0)
	}
	if errors.As(err, &exit) {
		if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			switch sig := status.Signal(); sig {
			case syscall.SIGKILL, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP, syscall.SIGPIPE, syscall.SIGALRM, syscall.SIGUSR1, syscall.SIGUSR2:
				// Die the same way, so the caller sees the signal.
				signal.Reset(sig)
				_ = unix.Kill(os.Getpid(), sig)
				time.Sleep(100 * time.Millisecond)
			}
			os.Exit(128 + int(status.Signal()))
		}
		os.Exit(exit.ExitCode())
	}
	launcherFail(err)
}

// runLauncherChild is the second stage: it confines itself with the ruleset
// on fd 3 (stacked on the supervisor's domain), installs the socket filter
// and the notification filter, hands the listener to the supervisor over fd 4
// and execs the command.
func runLauncherChild(args []string) {
	runtime.LockOSThread()
	if len(args) < 3 {
		launcherFail(errors.New("launcher child needs a spec, a program and its argv"))
	}
	var spec childSpec
	if err := json.Unmarshal([]byte(args[0]), &spec); err != nil {
		launcherFail(fmt.Errorf("bad launcher child spec: %w", err))
	}
	if err := unix.Prctl(unix.PR_SET_PDEATHSIG, uintptr(unix.SIGKILL), 0, 0, 0); err != nil {
		launcherFail(fmt.Errorf("set the parent death signal: %w", err))
	}
	if os.Getppid() != spec.Parent {
		launcherFail(errors.New("the sandbox supervisor exited"))
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		launcherFail(fmt.Errorf("set no_new_privs: %w", err))
	}
	sockets, err := seccompFilter(runtime.GOARCH, !spec.Network)
	if err != nil {
		launcherFail(err)
	}
	notify, err := notifyFilter(runtime.GOARCH)
	if err != nil {
		launcherFail(err)
	}
	if _, _, errno := unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, 3, 0, 0); errno != 0 {
		launcherFail(fmt.Errorf("enforce Landlock ruleset: %w", errno))
	}
	unix.Close(3)
	if err := installSeccomp(sockets); err != nil {
		launcherFail(err)
	}
	listener, err := installNotifyFilter(notify)
	if err != nil {
		launcherFail(err)
	}
	if err := unix.Sendmsg(4, []byte{0}, unix.UnixRights(listener), nil, 0); err != nil {
		launcherFail(fmt.Errorf("hand the seccomp listener to the supervisor: %w", err))
	}
	unix.Close(listener)
	unix.Close(4)
	err = unix.Exec(args[1], args[2:], os.Environ())
	fmt.Fprintf(os.Stderr, "%s: %s: %v\n", launcherMessagePrefix, args[1], err)
	if errors.Is(err, unix.ENOENT) {
		os.Exit(127)
	}
	os.Exit(126)
}

func installNotifyFilter(filter []bpfInstr) (int, error) {
	prog := make([]unix.SockFilter, len(filter))
	for i, in := range filter {
		prog[i] = unix.SockFilter{Code: in.Code, Jt: in.Jt, Jf: in.Jf, K: in.K}
	}
	fprog := unix.SockFprog{Len: uint16(len(prog)), Filter: &prog[0]}
	// WAIT_KILLABLE_RECV keeps a call the supervisor already took from being
	// interrupted (and restarted) by a non-fatal signal after it acted.
	flags := uintptr(unix.SECCOMP_FILTER_FLAG_NEW_LISTENER | unix.SECCOMP_FILTER_FLAG_WAIT_KILLABLE_RECV)
	fd, _, errno := unix.Syscall(unix.SYS_SECCOMP, unix.SECCOMP_SET_MODE_FILTER, flags, uintptr(unsafe.Pointer(&fprog)))
	if errno == unix.EINVAL {
		fd, _, errno = unix.Syscall(unix.SYS_SECCOMP, unix.SECCOMP_SET_MODE_FILTER, unix.SECCOMP_FILTER_FLAG_NEW_LISTENER, uintptr(unsafe.Pointer(&fprog)))
	}
	runtime.KeepAlive(&fprog)
	runtime.KeepAlive(prog)
	if errno != 0 {
		return -1, fmt.Errorf("install the seccomp notification filter: %w", errno)
	}
	return int(fd), nil
}

func receiveFD(sock int) (int, error) {
	buf := make([]byte, 1)
	oob := make([]byte, unix.CmsgSpace(4))
	_, oobn, _, _, err := unix.Recvmsg(sock, buf, oob, unix.MSG_CMSG_CLOEXEC)
	if err != nil {
		return -1, err
	}
	msgs, err := unix.ParseSocketControlMessage(oob[:oobn])
	if err != nil || len(msgs) == 0 {
		return -1, errors.New("no seccomp listener received")
	}
	fds, err := unix.ParseUnixRights(&msgs[0])
	if err != nil || len(fds) != 1 {
		return -1, errors.New("no seccomp listener received")
	}
	return fds[0], nil
}

var protectCheck struct {
	once sync.Once
	err  error
}

// landlockProtection reports whether the launcher can keep protected paths
// here, checked once per process by running a shell through it: the kernel
// needs Landlock ABI 3, seccomp user notification with descriptor injection
// (Linux 5.14) and must let the supervisor read its command's memory (Yama
// ptrace_scope 0 or 1). Inside another sandbox that already supervises
// seccomp notifications the listener cannot be installed and the check fails.
func landlockProtection() error {
	protectCheck.once.Do(func() { protectCheck.err = checkLandlockProtection() })
	return protectCheck.err
}

func checkLandlockProtection() error {
	if abi := landlockABI(); abi < minProtectABI {
		return fmt.Errorf("protected paths need Landlock ABI %d (Linux 6.2), this kernel has ABI %d", minProtectABI, abi)
	}
	if _, ok := seccompArches[runtime.GOARCH]; !ok {
		return fmt.Errorf("no seccomp filter for %s", runtime.GOARCH)
	}
	dir, err := os.MkdirTemp("", "k-brain-protect-check-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	guarded := filepath.Join(dir, ".k-brain")
	if err := os.Mkdir(guarded, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(guarded, "settings"), []byte("keep\n"), 0o644); err != nil {
		return err
	}
	spec, _ := json.Marshal(launchSpec{Write: []string{dir}, Protect: []string{guarded}})
	script := `cd "$1" || exit 2
echo ok > new || exit 3
mkdir sub && echo ok > sub/file || exit 4
{ echo bad > .k-brain/settings; } 2>/dev/null && exit 5
{ echo bad > .k-brain/new; } 2>/dev/null && exit 6
mv .k-brain moved 2>/dev/null && exit 7
exit 0`
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	check := exec.CommandContext(ctx, selfExe)
	check.Args = []string{launcherArg0, string(spec), "/bin/sh", "sh", "-c", script, "sh", dir}
	if out, err := check.CombinedOutput(); err != nil {
		return fmt.Errorf("the protected-path check failed: %s", firstLine(out, err))
	}
	for path, want := range map[string]string{"new": "ok\n", "sub/file": "ok\n", ".k-brain/settings": "keep\n"} {
		if got, err := os.ReadFile(filepath.Join(dir, path)); err != nil || string(got) != want {
			return fmt.Errorf("the protected-path check found %s changed", path)
		}
	}
	for _, path := range []string{".k-brain/new", "moved"} {
		if _, err := os.Lstat(filepath.Join(dir, path)); err == nil {
			return fmt.Errorf("the protected-path check found %s created", path)
		}
	}
	return nil
}
