//go:build linux

package shell

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Landlock takes TCP connect and bind away from a profile with no
// network (ABI 4), but nothing else: a UDP socket still sends (DNS
// included), and a unix socket still connects — to the docker daemon, an
// ssh agent, a D-Bus session — which for docker.sock is the whole
// machine. From ABI 6 Landlock can scope abstract unix sockets, but a
// socket with a path is still out of its reach at any ABI. So a
// no-network profile also gets a seccomp filter on socket(2): a command
// may make a TCP socket (which Landlock then keeps from connecting or
// binding) and a netlink one (the kernel's own interface table, which
// getaddrinfo reads), and nothing else. socketpair(2) is untouched: it
// joins two ends inside the sandbox and reaches nothing outside it.
//
// io_uring can create sockets without socket(2), so its setup call is
// refused too. Syscalls made through another ABI (a 32-bit binary's
// socketcall, x32) are refused wholesale, since the filter's numbers
// are the native ones.

// seccompAllowedFamilies documents the table below for the docs and tests.
var seccompAllowedFamilies = []string{"AF_INET/AF_INET6 with SOCK_STREAM and protocol 0 or TCP", "AF_NETLINK"}

// bpfProg assembles a classic BPF program with symbolic jump targets.
type bpfProg struct {
	ins    []unix.SockFilter
	labels map[string]int
	fixups []bpfFixup
}

type bpfFixup struct {
	at     int
	jt, jf string
}

func (p *bpfProg) label(name string) { p.labels[name] = len(p.ins) }

func (p *bpfProg) stmt(code uint16, k uint32) {
	p.ins = append(p.ins, unix.SockFilter{Code: code, K: k})
}

// jeq jumps to jt when A == k, else to jf; "" means the next instruction.
func (p *bpfProg) jeq(k uint32, jt, jf string) {
	p.fixups = append(p.fixups, bpfFixup{at: len(p.ins), jt: jt, jf: jf})
	p.ins = append(p.ins, unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: k})
}

func (p *bpfProg) jge(k uint32, jt, jf string) {
	p.fixups = append(p.fixups, bpfFixup{at: len(p.ins), jt: jt, jf: jf})
	p.ins = append(p.ins, unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JGE | unix.BPF_K, K: k})
}

func (p *bpfProg) link() error {
	for _, f := range p.fixups {
		for _, side := range []struct {
			name string
			dst  *uint8
		}{{f.jt, &p.ins[f.at].Jt}, {f.jf, &p.ins[f.at].Jf}} {
			if side.name == "" {
				*side.dst = 0
				continue
			}
			to, ok := p.labels[side.name]
			if !ok {
				return fmt.Errorf("bpf: no label %q", side.name)
			}
			off := to - f.at - 1
			if off < 0 || off > 255 {
				return fmt.Errorf("bpf: jump to %q out of range", side.name)
			}
			*side.dst = uint8(off)
		}
	}
	return nil
}

// seccomp_data offsets.
const (
	seccompNr   = 0
	seccompArch = 4
	seccompArg0 = 16 // low 32 bits on a little-endian machine
	seccompArg1 = 24
	seccompArg2 = 32
)

// noNetFilter builds the socket filter for this machine's architecture.
func noNetFilter() ([]unix.SockFilter, error) {
	if !seccompSupported {
		return nil, fmt.Errorf("no seccomp socket filter for this architecture")
	}
	ld := uint16(unix.BPF_LD | unix.BPF_W | unix.BPF_ABS)
	ret := uint16(unix.BPF_RET | unix.BPF_K)
	p := &bpfProg{labels: map[string]int{}}
	p.stmt(ld, seccompArch)
	p.jeq(auditArch, "", "foreign")
	p.stmt(ld, seccompNr)
	if x32Bit != 0 {
		p.jge(x32Bit, "foreign", "")
	}
	p.jeq(uint32(unix.SYS_SOCKET), "socket", "")
	p.jeq(uint32(unix.SYS_IO_URING_SETUP), "deny", "allow")
	// Classic BPF jumps only forward, so the returns come last.
	p.label("socket")
	p.stmt(ld, seccompArg0)
	p.jeq(unix.AF_NETLINK, "allow", "")
	p.jeq(unix.AF_INET, "inet", "")
	p.jeq(unix.AF_INET6, "inet", "deny")
	p.label("inet")
	p.stmt(ld, seccompArg1)
	p.stmt(unix.BPF_ALU|unix.BPF_AND|unix.BPF_K, 0xf) // SOCK_TYPE_MASK: drops NONBLOCK and CLOEXEC
	p.jeq(unix.SOCK_STREAM, "", "deny")
	p.stmt(ld, seccompArg2)
	p.jeq(0, "allow", "")
	p.jeq(unix.IPPROTO_TCP, "allow", "deny")
	p.label("allow")
	p.stmt(ret, unix.SECCOMP_RET_ALLOW)
	p.label("deny")
	p.stmt(ret, unix.SECCOMP_RET_ERRNO|uint32(unix.EACCES))
	p.label("foreign")
	p.stmt(ret, unix.SECCOMP_RET_ERRNO|uint32(unix.ENOSYS))
	if err := p.link(); err != nil {
		return nil, err
	}
	return p.ins, nil
}

// applyNoNetSeccomp installs the filter on the calling thread, which
// execve then hands to the command. no_new_privs must already be set.
func applyNoNetSeccomp() error {
	ins, err := noNetFilter()
	if err != nil {
		return err
	}
	prog := unix.SockFprog{Len: uint16(len(ins)), Filter: &ins[0]}
	if err := unix.Prctl(unix.PR_SET_SECCOMP, unix.SECCOMP_MODE_FILTER, uintptr(unsafe.Pointer(&prog)), 0, 0); err != nil {
		return fmt.Errorf("seccomp: %w", err)
	}
	return nil
}

// landlockSocketFilter reports whether a no-network profile under
// Landlock can have its non-TCP sockets taken away on this machine.
const landlockSocketFilter = seccompSupported
