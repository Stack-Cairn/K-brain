package sandbox

import (
	"encoding/binary"
	"testing"
)

// seccompData mirrors struct seccomp_data for the interpreter below.
type seccompData struct {
	nr   uint32
	arch uint32
	args [6]uint64
}

func (d seccompData) bytes() []byte {
	b := make([]byte, 64)
	binary.LittleEndian.PutUint32(b[0:], d.nr)
	binary.LittleEndian.PutUint32(b[4:], d.arch)
	for i, arg := range d.args {
		binary.LittleEndian.PutUint64(b[16+8*i:], arg)
	}
	return b
}

// runBPF interprets the subset of classic BPF the filter uses and returns the
// verdict, failing the test on anything the kernel would reject.
func runBPF(t *testing.T, prog []bpfInstr, d seccompData) uint32 {
	t.Helper()
	if len(prog) == 0 || len(prog) > 4096 {
		t.Fatalf("program length %d", len(prog))
	}
	data := d.bytes()
	var acc uint32
	for pc := 0; pc < len(prog); pc++ {
		in := prog[pc]
		switch in.Code {
		case bpfLdWAbs:
			if in.K%4 != 0 || int(in.K)+4 > len(data) {
				t.Fatalf("pc %d: load at %d", pc, in.K)
			}
			acc = binary.LittleEndian.Uint32(data[in.K:])
		case bpfJeqK, bpfJsetK:
			taken := acc == in.K
			if in.Code == bpfJsetK {
				taken = acc&in.K != 0
			}
			offset := int(in.Jf)
			if taken {
				offset = int(in.Jt)
			}
			if pc+offset+1 >= len(prog) {
				t.Fatalf("pc %d: jump past the end", pc)
			}
			pc += offset
		case bpfRetK:
			return in.K
		default:
			t.Fatalf("pc %d: unexpected opcode %#x", pc, in.Code)
		}
	}
	t.Fatal("program fell off the end")
	return 0
}

func TestSeccompFilterVerdicts(t *testing.T) {
	const (
		allow  = seccompRetA
		deny   = seccompRetE | errnoEPERM
		afInet = 2
		afNl   = 16
		read   = 0 // read(2) on amd64; any unfiltered call
		tcgets = 0x5401
	)
	for goarch, arch := range seccompArches {
		for _, offline := range []bool{true, false} {
			prog, err := seccompFilter(goarch, offline)
			if err != nil {
				t.Fatalf("%s: %v", goarch, err)
			}
			ipVerdict := uint32(allow)
			if offline {
				ipVerdict = deny
			}
			cases := []struct {
				name string
				data seccompData
				want uint32
			}{
				{"plain call", seccompData{nr: read, arch: arch.audit}, allow},
				{"foreign arch", seccompData{nr: read, arch: 0x40000003}, deny},
				{"unix socket", seccompData{nr: arch.socket, arch: arch.audit, args: [6]uint64{afUnix}}, allow},
				{"unix socketpair", seccompData{nr: arch.socketpair, arch: arch.audit, args: [6]uint64{afUnix}}, allow},
				{"inet socket", seccompData{nr: arch.socket, arch: arch.audit, args: [6]uint64{afInet}}, ipVerdict},
				{"netlink socket", seccompData{nr: arch.socket, arch: arch.audit, args: [6]uint64{afNl}}, ipVerdict},
				{"inet socketpair", seccompData{nr: arch.socketpair, arch: arch.audit, args: [6]uint64{afInet}}, ipVerdict},
				{"vsock socket", seccompData{nr: arch.socket, arch: arch.audit, args: [6]uint64{afVsock}}, deny},
				{"io_uring_setup", seccompData{nr: arch.ioUringSetup, arch: arch.audit}, deny},
				{"io_uring_enter", seccompData{nr: arch.ioUringEnter, arch: arch.audit}, deny},
				{"io_uring_register", seccompData{nr: arch.ioUringRegist, arch: arch.audit}, deny},
				{"TIOCSTI", seccompData{nr: arch.ioctl, arch: arch.audit, args: [6]uint64{0, tiocsti}}, deny},
				{"TIOCLINUX", seccompData{nr: arch.ioctl, arch: arch.audit, args: [6]uint64{0, tioclinux}}, deny},
				{"TCGETS", seccompData{nr: arch.ioctl, arch: arch.audit, args: [6]uint64{0, tcgets}}, allow},
			}
			if arch.x32 {
				cases = append(cases, struct {
					name string
					data seccompData
					want uint32
				}{"x32 call", seccompData{nr: x32Bit | read, arch: arch.audit}, deny})
			}
			for _, tc := range cases {
				if got := runBPF(t, prog, tc.data); got != tc.want {
					t.Errorf("%s offline=%v %s: verdict %#x, want %#x", goarch, offline, tc.name, got, tc.want)
				}
			}
		}
	}
}

func TestSeccompFilterUnknownArch(t *testing.T) {
	if _, err := seccompFilter("mips", true); err == nil {
		t.Fatal("filter built for an unsupported architecture")
	}
}

func TestBPFAssemblerRejectsBadJumps(t *testing.T) {
	var missing bpfProgram
	missing.jump(bpfJeqK, 1, "nowhere", "")
	if _, err := missing.assemble(); err == nil {
		t.Fatal("jump to a missing label assembled")
	}
	var backward bpfProgram
	backward.label("top")
	backward.stmt(bpfLdWAbs, seccompNr)
	backward.jump(bpfJeqK, 1, "top", "")
	if _, err := backward.assemble(); err == nil {
		t.Fatal("backward jump assembled")
	}
}
