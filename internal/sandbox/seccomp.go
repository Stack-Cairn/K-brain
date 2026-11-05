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
}

var seccompArches = map[string]seccompArch{
	"amd64": {audit: 0xc000003e, x32: true, socket: 41, socketpair: 53, ioctl: 16, ioUringSetup: 425, ioUringEnter: 426, ioUringRegist: 427},
	"arm64": {audit: 0xc00000b7, socket: 198, socketpair: 199, ioctl: 29, ioUringSetup: 425, ioUringEnter: 426, ioUringRegist: 427},
}

const (
	bpfLdWAbs   = 0x20 // BPF_LD | BPF_W | BPF_ABS
	bpfJeqK     = 0x15 // BPF_JMP | BPF_JEQ | BPF_K
	bpfJsetK    = 0x45 // BPF_JMP | BPF_JSET | BPF_K
	bpfRetK     = 0x06 // BPF_RET | BPF_K
	seccompRetA = 0x7fff0000
	seccompRetE = 0x00050000 // SECCOMP_RET_ERRNO
	errnoEPERM  = 1

	// struct seccomp_data offsets; arguments are 64-bit and both supported
	// architectures are little-endian, so +0 is the low word the kernel uses
	// for int and unsigned int parameters.
	seccompNr         = 0
	seccompArchOffset = 4
	seccompArg0       = 16
	seccompArg1       = 24

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
