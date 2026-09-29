//go:build linux && !amd64 && !arm64

package shell

// On other architectures the socket filter is not built, and a profile
// with no network is refused under Landlock rather than run with unix
// sockets and UDP open.
const (
	seccompSupported = false
	auditArch        = 0
	x32Bit           = 0
)
