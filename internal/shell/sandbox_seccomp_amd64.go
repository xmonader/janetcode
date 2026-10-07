//go:build linux && amd64

package shell

import "golang.org/x/sys/unix"

const (
	seccompSupported = true
	auditArch        = unix.AUDIT_ARCH_X86_64
	// x32Bit marks the x32 ABI's syscall numbers, which share the x86_64
	// arch value and would otherwise slip past the native numbers.
	x32Bit = 0x40000000
)
