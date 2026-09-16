//go:build linux

package shell

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// The Landlock backend needs its rules applied between fork and exec,
// and os/exec has no hook there. So the sandboxed command is started as
// this same binary, re-executed with landlockSentinel as its first
// argument and the policy in landlockEnv: the init below runs before
// main, locks itself into the Landlock domain, and execs the real
// command, which inherits the domain. Every binary that imports this
// package carries the hook, so it works in the test binary too.
const (
	landlockSentinel = "__janetcode_landlock_exec__"
	landlockEnv      = "JANETCODE_LANDLOCK_POLICY"
)

// landlockSpec is the policy handed to the re-executed process.
type landlockSpec struct {
	// Write are the directories that stay writable.
	Write []string `json:"write"`
	// Net reports whether the command may use the network. Taking it
	// away needs ABI 4; the caller has already refused otherwise.
	Net bool `json:"net"`
}

// Landlock's ABI 1 write accesses. Read and execute are deliberately not
// handled: a profile's reads are broad, and a ruleset that does not
// handle an access does not restrict it.
const landlockFSWrite = unix.LANDLOCK_ACCESS_FS_WRITE_FILE |
	unix.LANDLOCK_ACCESS_FS_REMOVE_DIR |
	unix.LANDLOCK_ACCESS_FS_REMOVE_FILE |
	unix.LANDLOCK_ACCESS_FS_MAKE_CHAR |
	unix.LANDLOCK_ACCESS_FS_MAKE_DIR |
	unix.LANDLOCK_ACCESS_FS_MAKE_REG |
	unix.LANDLOCK_ACCESS_FS_MAKE_SOCK |
	unix.LANDLOCK_ACCESS_FS_MAKE_FIFO |
	unix.LANDLOCK_ACCESS_FS_MAKE_BLOCK |
	unix.LANDLOCK_ACCESS_FS_MAKE_SYM

// landlockABI reports the kernel's Landlock ABI version, 0 for a kernel
// without it.
func landlockABI() int {
	r, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, unix.LANDLOCK_CREATE_RULESET_VERSION)
	if errno != 0 {
		return 0
	}
	return int(r)
}

// handledAccess is the access mask a ruleset governs at this ABI, and
// the mask granted on each write root.
func handledAccess(abi int) uint64 {
	access := uint64(landlockFSWrite)
	if abi >= 2 {
		// Without REFER handled, renaming or linking across directories
		// is denied outright — including between two directories the
		// profile makes writable.
		access |= unix.LANDLOCK_ACCESS_FS_REFER
	}
	if abi >= 3 {
		access |= unix.LANDLOCK_ACCESS_FS_TRUNCATE
	}
	return access
}

// landlockDevices stay writable under every profile. They are not the
// user's data: a shell that cannot write /dev/null fails at "> /dev/null",
// and one that cannot write its own terminal or /dev/fd cannot report
// anything at all. Nothing here is a path a command could keep.
var landlockDevices = []string{
	"/dev/null", "/dev/zero", "/dev/full", "/dev/random", "/dev/urandom",
	"/dev/tty", "/dev/ptmx", "/dev/pts", "/dev/shm", "/dev/fd", "/proc/self/fd",
}

func init() {
	if len(os.Args) < 3 || os.Args[1] != landlockSentinel {
		return
	}
	// This process exists only to become the sandboxed command.
	runtime.LockOSThread()
	spec := landlockSpec{}
	if raw := os.Getenv(landlockEnv); raw != "" {
		if err := json.Unmarshal([]byte(raw), &spec); err != nil {
			landlockDie(fmt.Errorf("unreadable policy: %w", err))
		}
	}
	if err := applyLandlock(spec); err != nil {
		landlockDie(err)
	}
	_ = os.Unsetenv(landlockEnv)
	argv := os.Args[2:]
	if err := syscall.Exec(argv[0], argv, os.Environ()); err != nil {
		landlockDie(fmt.Errorf("exec %s: %w", argv[0], err))
	}
}

func landlockDie(err error) {
	fmt.Fprintln(os.Stderr, "sandbox (landlock): "+err.Error())
	os.Exit(126)
}

// applyLandlock puts the calling thread in a Landlock domain that lets
// writes happen only beneath spec.Write, and — from ABI 4 — takes TCP
// away when the profile has no network. execve inherits the domain, so
// the command started next is inside it.
func applyLandlock(spec landlockSpec) error {
	abi := landlockABI()
	if abi < 1 {
		return fmt.Errorf("this kernel has no Landlock")
	}
	attr := unix.LandlockRulesetAttr{Access_fs: handledAccess(abi)}
	if !spec.Net {
		if abi < 4 {
			return fmt.Errorf("this kernel's Landlock ABI is %d, which cannot take the network away (needs ABI 4)", abi)
		}
		attr.Access_net = unix.LANDLOCK_ACCESS_NET_BIND_TCP | unix.LANDLOCK_ACCESS_NET_CONNECT_TCP
	}
	fd, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET,
		uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr), 0)
	if errno != 0 {
		return fmt.Errorf("landlock_create_ruleset: %w", errno)
	}
	defer unix.Close(int(fd))
	for _, dir := range append(append([]string(nil), spec.Write...), landlockDevices...) {
		if err := landlockAllow(int(fd), dir, handledAccess(abi)); err != nil {
			return err
		}
	}
	// Landlock refuses to restrict a thread that could still gain
	// privileges through a setuid binary.
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("prctl(no_new_privs): %w", err)
	}
	if _, _, errno := unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, fd, 0, 0); errno != 0 {
		return fmt.Errorf("landlock_restrict_self: %w", errno)
	}
	return nil
}

// pathBeneath mirrors the kernel's packed struct landlock_path_beneath_attr.
// The kernel reads the first 12 bytes
// of what the pointer names, so the padding Go adds for alignment is
// never looked at.
type pathBeneath struct {
	access uint64
	fd     int32
	_      [4]byte
}

func landlockAllow(ruleset int, dir string, access uint64) error {
	f, err := os.OpenFile(dir, unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		// A write root that is not there (an extra dir that was removed)
		// is not a reason to run unsandboxed, or to fail: it simply
		// grants nothing.
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("open %s: %w", dir, err)
	}
	defer f.Close()
	// The kernel refuses a rule that grants a directory-only access on a
	// file, so a device node or a regular file keeps only the two
	// accesses that can apply to it.
	if fi, err := f.Stat(); err == nil && !fi.IsDir() {
		access &= unix.LANDLOCK_ACCESS_FS_WRITE_FILE | unix.LANDLOCK_ACCESS_FS_TRUNCATE
		if access == 0 {
			return nil
		}
	}
	rule := pathBeneath{access: access, fd: int32(f.Fd())}
	_, _, errno := unix.Syscall6(unix.SYS_LANDLOCK_ADD_RULE,
		uintptr(ruleset), unix.LANDLOCK_RULE_PATH_BENEATH,
		uintptr(unsafe.Pointer(&rule)), 0, 0, 0)
	if errno != 0 {
		return fmt.Errorf("landlock_add_rule %s: %w", dir, errno)
	}
	return nil
}

// landlock builds the command as a re-exec of this binary carrying the
// profile's policy (see the comment on landlockSentinel).
func (s *Sandbox) landlock(ctx context.Context, p Profile, root string, extra []string, command string) (*exec.Cmd, error) {
	abi := s.landlockABI()
	if abi < 1 {
		return nil, fmt.Errorf("this kernel has no Landlock (needs Linux 5.13 or newer with the LSM enabled)")
	}
	if !p.Network && abi < 4 {
		return nil, fmt.Errorf("the %s profile needs the network taken away, and Landlock ABI %d cannot do that (needs ABI 4, Linux 6.7)", p.Name, abi)
	}
	self, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("landlock: cannot re-execute this binary: %w", err)
	}
	sh := Default()
	argv := append([]string{landlockSentinel, sh.Path}, sh.Args...)
	argv = append(argv, command)
	policy, err := json.Marshal(landlockSpec{Write: p.WritableRoots(root, extra, os.TempDir()), Net: p.Network})
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, self, argv...)
	cmd.Dir = root
	cmd.Env = append(ScrubEnv(os.Environ()), landlockEnv+"="+string(policy))
	SetProcessGroup(cmd)
	return cmd, nil
}
