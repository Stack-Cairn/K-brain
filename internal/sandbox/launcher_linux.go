//go:build linux

package sandbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

// A binary that imports this package doubles as the Landlock launcher: Wrap
// runs /proc/self/exe with launcherArg0 as argv[0], and init below confines
// the process and execs the command before anything else starts. Landlock and
// seccomp restrictions survive execve, so the command and all its descendants
// stay confined, while the calling K-brain process is left untouched.
func init() {
	if len(os.Args) == 0 {
		return
	}
	switch os.Args[0] {
	case launcherArg0:
		runLauncher(os.Args[1:])
	case launcherChildArg0:
		runLauncherChild(os.Args[1:])
	}
}

// runLauncher never returns. Its arguments are the JSON spec, the program path
// and the program's argv.
func runLauncher(args []string) {
	// Landlock, no_new_privs and seccomp apply to the calling thread; execve
	// from that same thread carries them into the command.
	runtime.LockOSThread()
	if len(args) < 3 {
		launcherFail(errors.New("launcher needs a spec, a program and its argv"))
	}
	var spec launchSpec
	if err := json.Unmarshal([]byte(args[0]), &spec); err != nil {
		launcherFail(fmt.Errorf("bad launcher spec: %w", err))
	}
	if len(spec.Protect) > 0 {
		runSupervisor(spec, args[1], args[2:])
	}
	if err := confineSelf(spec); err != nil {
		launcherFail(err)
	}
	err := unix.Exec(args[1], args[2:], os.Environ())
	fmt.Fprintf(os.Stderr, "%s: %s: %v\n", launcherMessagePrefix, args[1], err)
	if errors.Is(err, unix.ENOENT) {
		os.Exit(127)
	}
	os.Exit(126)
}

func launcherFail(err error) {
	fmt.Fprintf(os.Stderr, "%s: %v; the command was not run\n", launcherMessagePrefix, err)
	os.Exit(launcherFailedExit)
}

// confineSelf restricts the calling thread: no_new_privs, a Landlock ruleset
// that only allows writes beneath spec.Write, and the seccomp filter. Any
// failure is returned so the command is not run unconfined.
func confineSelf(spec launchSpec) error {
	abi := landlockABI()
	if abi < 1 {
		return errors.New("Landlock is not available in this kernel")
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("set no_new_privs: %w", err)
	}
	filter, err := seccompFilter(runtime.GOARCH, !spec.Network)
	if err != nil {
		return err
	}
	if err := restrictWrites(abi, spec); err != nil {
		return err
	}
	return installSeccomp(filter)
}

// landlockABI returns the kernel's Landlock ABI version, 0 when unavailable.
func landlockABI() int {
	v, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, unix.LANDLOCK_CREATE_RULESET_VERSION)
	if errno != 0 {
		return 0
	}
	return int(v)
}

// landlockWriteAccess is every write-side filesystem right the ABI knows.
// Reads and execution stay unhandled, so they remain allowed everywhere.
// IOCTL_DEV (ABI 5) stays unhandled too: ioctls on terminals are needed by
// interactive commands and the dangerous ones are filtered by seccomp.
func landlockWriteAccess(abi int) uint64 {
	access := uint64(unix.LANDLOCK_ACCESS_FS_WRITE_FILE | unix.LANDLOCK_ACCESS_FS_REMOVE_DIR |
		unix.LANDLOCK_ACCESS_FS_REMOVE_FILE | unix.LANDLOCK_ACCESS_FS_MAKE_CHAR |
		unix.LANDLOCK_ACCESS_FS_MAKE_DIR | unix.LANDLOCK_ACCESS_FS_MAKE_REG |
		unix.LANDLOCK_ACCESS_FS_MAKE_SOCK | unix.LANDLOCK_ACCESS_FS_MAKE_FIFO |
		unix.LANDLOCK_ACCESS_FS_MAKE_BLOCK | unix.LANDLOCK_ACCESS_FS_MAKE_SYM)
	if abi >= 2 {
		// Without REFER handled, ABI 1 refuses every cross-directory rename
		// and link, even inside the grants.
		access |= unix.LANDLOCK_ACCESS_FS_REFER
	}
	if abi >= 3 {
		access |= unix.LANDLOCK_ACCESS_FS_TRUNCATE
	}
	return access
}

// landlockFileAccess are the rights a rule on a non-directory may carry.
const landlockFileAccess = unix.LANDLOCK_ACCESS_FS_WRITE_FILE | unix.LANDLOCK_ACCESS_FS_TRUNCATE

func restrictWrites(abi int, spec launchSpec) error {
	access := landlockWriteAccess(abi)
	attr := unix.LandlockRulesetAttr{Access_fs: access}
	if !spec.Network && abi >= 4 {
		// Defense in depth next to seccomp: no TCP bind or connect at all.
		attr.Access_net = unix.LANDLOCK_ACCESS_NET_BIND_TCP | unix.LANDLOCK_ACCESS_NET_CONNECT_TCP
	}
	if abi >= 6 {
		// Signals to processes outside the sandbox (K-brain itself) fail.
		attr.Scoped = unix.LANDLOCK_SCOPE_SIGNAL
		if !spec.Network {
			// Abstract unix sockets are a network namespace resource; an
			// offline run gets none, as under a network namespace.
			attr.Scoped |= unix.LANDLOCK_SCOPE_ABSTRACT_UNIX_SOCKET
		}
	}
	size := landlockRulesetAttrSize(abi)
	fd, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, uintptr(unsafe.Pointer(&attr)), size, 0)
	if errno != 0 {
		return fmt.Errorf("create Landlock ruleset (ABI %d): %w", abi, errno)
	}
	ruleset := int(fd)
	defer unix.Close(ruleset)
	for _, path := range spec.Write {
		if err := addWriteRule(ruleset, path, access); err != nil {
			return err
		}
	}
	if _, _, errno := unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, uintptr(ruleset), 0, 0); errno != 0 {
		return fmt.Errorf("enforce Landlock ruleset: %w", errno)
	}
	return nil
}

// landlockRulesetAttrSize is the size of the ruleset attribute the ABI knows:
// access_fs (1), plus access_net (4), plus scoped (6).
func landlockRulesetAttrSize(abi int) uintptr {
	switch {
	case abi >= 6:
		return 24
	case abi >= 4:
		return 16
	default:
		return 8
	}
}

// addWriteRule grants writes beneath path. A missing path is skipped: the
// optional grants (temporary and device directories) vary between machines,
// and the workspace root is checked by the caller.
func addWriteRule(ruleset int, path string, access uint64) error {
	fd, err := unix.Open(path, unix.O_PATH|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open %s for a Landlock rule: %w", path, err)
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR {
		access &= landlockFileAccess
	}
	rule := unix.LandlockPathBeneathAttr{Allowed_access: access, Parent_fd: int32(fd)}
	if _, _, errno := unix.Syscall6(unix.SYS_LANDLOCK_ADD_RULE, uintptr(ruleset), unix.LANDLOCK_RULE_PATH_BENEATH, uintptr(unsafe.Pointer(&rule)), 0, 0, 0); errno != 0 {
		return fmt.Errorf("add Landlock rule for %s: %w", path, errno)
	}
	return nil
}

func installSeccomp(filter []bpfInstr) error {
	prog := make([]unix.SockFilter, len(filter))
	for i, in := range filter {
		prog[i] = unix.SockFilter{Code: in.Code, Jt: in.Jt, Jf: in.Jf, K: in.K}
	}
	fprog := unix.SockFprog{Len: uint16(len(prog)), Filter: &prog[0]}
	err := unix.Prctl(unix.PR_SET_SECCOMP, unix.SECCOMP_MODE_FILTER, uintptr(unsafe.Pointer(&fprog)), 0, 0)
	runtime.KeepAlive(&fprog)
	runtime.KeepAlive(prog)
	if err != nil {
		return fmt.Errorf("install seccomp filter: %w", err)
	}
	return nil
}
