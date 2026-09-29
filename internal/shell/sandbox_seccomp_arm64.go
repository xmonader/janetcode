//go:build linux && arm64

package shell

import "golang.org/x/sys/unix"

const (
	seccompSupported = true
	auditArch        = unix.AUDIT_ARCH_AARCH64
	x32Bit           = 0
)
