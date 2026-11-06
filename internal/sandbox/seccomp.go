package sandbox

import "fmt"

// The seccomp filter installed by the Landlock launcher. Landlock fences the
// filesystem only; the filter adds what a mount and network namespace would
// otherwise provide:
//
//   - socket(2) and socketpair(2) for any family but AF_UNIX fail when the run
//     is offline, so no IP, netlink, packet or vsock socket can be created;
//   - AF_VSOCK always fails: under WSL2 it reaches the Windows host, where
//     interop starts processes outside the sandbox;
//   - io_uring is unavailable, because its socket and connect operations do
//     not pass through the filtered system calls;
//   - the TIOCSTI and TIOCLINUX ioctls fail, so a command cannot push input
//     into the terminal K-brain runs in.
//
// Denied calls return EPERM. A call made through another ABI (i386 or x32 on
// amd64) is denied wholesale: its system call numbers mean different calls.
//
// The program is built from constants rather than x/sys values so it can be
// unit tested on every platform.

type seccompArch struct {
	audit uint32
	// x32 marks amd64, whose x32 ABI shares the audit arch and sets bit 30 of
	// the system call number.
	x32           bool
	socket        uint32
	socketpair    uint32
	ioctl         uint32
	ioUringSetup  uint32
	ioUringEnter  uint32
	ioUringRegist uint32
	// fs lists the file system calls the protected-path supervisor handles
	// (supervise_linux.go); a zero number is a call the architecture lacks.
	fs fsCalls
	// xattrSet are the calls that set or remove extended attributes.
	xattrSet []uint32
}

// fsCalls are the system call numbers of the calls that create, remove,
// rename, link, open for writing, truncate or change the mode of a path.
type fsCalls struct {
	open, creat, openat, openat2               uint32
	mkdir, mkdirat, mknod, mknodat             uint32
	unlink, unlinkat, rmdir                    uint32
	rename, renameat, renameat2                uint32
	link, linkat, symlink, symlinkat, truncate uint32
	chmod, fchmod, fchmodat, fchmodat2         uint32
}

var seccompArches = map[string]seccompArch{
	"amd64": {audit: 0xc000003e, x32: true, socket: 41, socketpair: 53, ioctl: 16, ioUringSetup: 425, ioUringEnter: 426, ioUringRegist: 427,
		fs: fsCalls{open: 2, creat: 85, openat: 257, openat2: 437, mkdir: 83, mkdirat: 258, mknod: 133, mknodat: 259,
			unlink: 87, unlinkat: 263, rmdir: 84, rename: 82, renameat: 264, renameat2: 316,
			link: 86, linkat: 265, symlink: 88, symlinkat: 266, truncate: 76,
			chmod: 90, fchmod: 91, fchmodat: 268, fchmodat2: 452},
		xattrSet: []uint32{188, 189, 190, 197, 198, 199, 463, 466}},
	"arm64": {audit: 0xc00000b7, socket: 198, socketpair: 199, ioctl: 29, ioUringSetup: 425, ioUringEnter: 426, ioUringRegist: 427,
		fs: fsCalls{openat: 56, openat2: 437, mkdirat: 34, mknodat: 33, unlinkat: 35, renameat: 38, renameat2: 276,
			linkat: 37, symlinkat: 36, truncate: 45, fchmod: 52, fchmodat: 53, fchmodat2: 452},
		xattrSet: []uint32{5, 6, 7, 14, 15, 16, 463, 466}},
}

const (
	bpfLdWAbs    = 0x20 // BPF_LD | BPF_W | BPF_ABS
	bpfJeqK      = 0x15 // BPF_JMP | BPF_JEQ | BPF_K
	bpfJsetK     = 0x45 // BPF_JMP | BPF_JSET | BPF_K
	bpfRetK      = 0x06 // BPF_RET | BPF_K
	seccompRetA  = 0x7fff0000
	seccompRetE  = 0x00050000 // SECCOMP_RET_ERRNO
	seccompRetU  = 0x7fc00000 // SECCOMP_RET_USER_NOTIF
	errnoEPERM   = 1
	errnoENOTSUP = 95

	// struct seccomp_data offsets; arguments are 64-bit and both supported
	// architectures are little-endian, so +0 is the low word the kernel uses
	// for int and unsigned int parameters.
	seccompNr         = 0
	seccompArchOffset = 4
	seccompArg0       = 16
	seccompArg1       = 24

	seccompArg2 = 32

	// Open flags that can change a file: O_WRONLY, O_RDWR, O_CREAT, O_TRUNC
	// and __O_TMPFILE, with the same values on both architectures.
	openWriteFlags = 0x1 | 0x2 | 0x40 | 0x200 | 0x400000

	afUnix    = 1
	afVsock   = 40
	tiocsti   = 0x5412
	tioclinux = 0x541c
	x32Bit    = 0x40000000
)

type bpfInstr struct {
	Code uint16
	Jt   uint8
	Jf   uint8
	K    uint32
}

// bpfProgram assembles straight-line code with forward jumps to labels.
type bpfProgram struct {
	code   []bpfInstr
	fixups []bpfFixup
	labels map[string]int
}

type bpfFixup struct {
	at     int
	tLabel string
	fLabel string
}

func (b *bpfProgram) stmt(code uint16, k uint32) {
	b.code = append(b.code, bpfInstr{Code: code, K: k})
}

// jump appends a conditional jump; an empty label falls through.
func (b *bpfProgram) jump(code uint16, k uint32, onTrue, onFalse string) {
	b.fixups = append(b.fixups, bpfFixup{at: len(b.code), tLabel: onTrue, fLabel: onFalse})
	b.code = append(b.code, bpfInstr{Code: code, K: k})
}

func (b *bpfProgram) label(name string) {
	if b.labels == nil {
		b.labels = map[string]int{}
	}
	b.labels[name] = len(b.code)
}

func (b *bpfProgram) assemble() ([]bpfInstr, error) {
	for _, f := range b.fixups {
		for _, target := range []struct {
			label string
			field *uint8
		}{{f.tLabel, &b.code[f.at].Jt}, {f.fLabel, &b.code[f.at].Jf}} {
			if target.label == "" {
				continue
			}
			pos, ok := b.labels[target.label]
			offset := pos - f.at - 1
			if !ok || offset < 0 || offset > 255 {
				return nil, fmt.Errorf("seccomp: bad jump to %q", target.label)
			}
			*target.field = uint8(offset)
		}
	}
	return b.code, nil
}

// seccompFilter returns the filter for goarch, or an error when the
// architecture is not supported.
func seccompFilter(goarch string, offline bool) ([]bpfInstr, error) {
	arch, ok := seccompArches[goarch]
	if !ok {
		return nil, fmt.Errorf("seccomp filtering is not supported on %s", goarch)
	}
	var b bpfProgram
	b.stmt(bpfLdWAbs, seccompArchOffset)
	b.jump(bpfJeqK, arch.audit, "", "deny")
	b.stmt(bpfLdWAbs, seccompNr)
	if arch.x32 {
		b.jump(bpfJsetK, x32Bit, "deny", "")
	}
	for _, nr := range []uint32{arch.ioUringSetup, arch.ioUringEnter, arch.ioUringRegist} {
		b.jump(bpfJeqK, nr, "deny", "")
	}
	b.jump(bpfJeqK, arch.socket, "socket", "")
	b.jump(bpfJeqK, arch.socketpair, "socket", "")
	b.jump(bpfJeqK, arch.ioctl, "ioctl", "")
	b.stmt(bpfRetK, seccompRetA)

	b.label("socket")
	b.stmt(bpfLdWAbs, seccompArg0)
	if offline {
		b.jump(bpfJeqK, afUnix, "allow", "deny")
	} else {
		b.jump(bpfJeqK, afVsock, "deny", "allow")
	}

	b.label("ioctl")
	b.stmt(bpfLdWAbs, seccompArg1)
	b.jump(bpfJeqK, tiocsti, "deny", "")
	b.jump(bpfJeqK, tioclinux, "deny", "allow")

	// BPF jumps only forward, so both verdicts sit at the end.
	b.label("allow")
	b.stmt(bpfRetK, seccompRetA)
	b.label("deny")
	b.stmt(bpfRetK, seccompRetE|errnoEPERM)
	return b.assemble()
}

// notifyFilter returns the filter that hands the file system calls of
// fsCalls to the protected-path supervisor (SECCOMP_RET_USER_NOTIF). Opens
// are handed over only with a flag that can change the file; openat2 keeps its
// flags in memory, so every openat2 is. Setting or removing an extended
// attribute fails with ENOTSUP: an access ACL is a mode change the supervisor
// would have to check and the call is rare. Calls through another ABI are left
// to seccompFilter, which denies them.
func notifyFilter(goarch string) ([]bpfInstr, error) {
	arch, ok := seccompArches[goarch]
	if !ok {
		return nil, fmt.Errorf("seccomp filtering is not supported on %s", goarch)
	}
	var b bpfProgram
	b.stmt(bpfLdWAbs, seccompArchOffset)
	b.jump(bpfJeqK, arch.audit, "", "allow")
	b.stmt(bpfLdWAbs, seccompNr)
	fs := arch.fs
	for _, nr := range []uint32{fs.creat, fs.openat2, fs.mkdir, fs.mkdirat, fs.mknod, fs.mknodat,
		fs.unlink, fs.unlinkat, fs.rmdir, fs.rename, fs.renameat, fs.renameat2,
		fs.link, fs.linkat, fs.symlink, fs.symlinkat, fs.truncate,
		fs.chmod, fs.fchmod, fs.fchmodat, fs.fchmodat2} {
		if nr != 0 {
			b.jump(bpfJeqK, nr, "notify", "")
		}
	}
	b.jump(bpfJeqK, fs.openat, "flags2", "")
	if fs.open != 0 {
		b.jump(bpfJeqK, fs.open, "flags1", "")
	}
	for _, nr := range arch.xattrSet {
		b.jump(bpfJeqK, nr, "enotsup", "")
	}
	b.stmt(bpfRetK, seccompRetA)

	b.label("flags1")
	b.stmt(bpfLdWAbs, seccompArg1)
	b.jump(bpfJsetK, openWriteFlags, "notify", "allow")
	b.label("flags2")
	b.stmt(bpfLdWAbs, seccompArg2)
	b.jump(bpfJsetK, openWriteFlags, "notify", "allow")

	b.label("notify")
	b.stmt(bpfRetK, seccompRetU)
	b.label("enotsup")
	b.stmt(bpfRetK, seccompRetE|errnoENOTSUP)
	b.label("allow")
	b.stmt(bpfRetK, seccompRetA)
	return b.assemble()
}
