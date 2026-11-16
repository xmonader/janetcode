package shell

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"sync"
)

// DefaultSandboxImage is used when /sandbox is on and no image was named.
const DefaultSandboxImage = "alpine:3.20"

// Backend names. "auto" picks one for the machine: seatbelt on macOS,
// docker on Linux when it is installed, Landlock otherwise.
const (
	BackendAuto     = "auto"
	BackendDocker   = "docker"
	BackendSeatbelt = "seatbelt"
	BackendLandlock = "landlock"
	BackendBwrap    = "bwrap"
)

// Backends are the names /sandbox accepts to force one.
var Backends = []string{BackendAuto, BackendBwrap, BackendDocker, BackendSeatbelt, BackendLandlock}

// Sandbox, when enabled, wraps local bash in one of three backends —
// docker, macOS seatbelt, or Linux Landlock — applying a named Profile
// (see profile.go) as far as the backend can. File tools stay on the
// workspace host, including after /ssh or /docker, so the sandbox is
// only around the shell. Off by default.
type Sandbox struct {
	mu      sync.Mutex
	on      bool
	image   string
	profile string
	// backend, when set, forces one instead of picking for the machine.
	backend string
	// look, when set, replaces exec.LookPath (tests).
	look func(string) (string, error)
	// abi, when non-nil, replaces the Landlock ABI probe (tests).
	abi func() int
	// probe, when non-nil, replaces the check that bwrap can run (tests).
	probe func() bool
	// forced is the profile a managed policy fixes (see Force): the
	// sandbox cannot be turned off or moved to another profile.
	forced, forcedBy string
	// proxy, when set, is the egress proxy a profile with a network is
	// narrowed to (see sandbox_egress.go).
	proxy EgressProxy
}

// Force fixes the sandbox on at profile for the rest of the session:
// /sandbox off and a switch to any other profile are refused with why,
// and Disable does nothing. It is what a managed policy's "sandbox" key
// does. It does not enable the sandbox; the caller does that.
func (s *Sandbox) Force(profile, why string) error {
	if err := s.SetProfile(profile); err != nil {
		return err
	}
	s.mu.Lock()
	s.forced, s.forcedBy = profile, why
	s.mu.Unlock()
	return nil
}

// Forced is the profile Force fixed, or "".
func (s *Sandbox) Forced() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.forced
}

func (s *Sandbox) forcedErr(what string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.forced == "" {
		return nil
	}
	msg := fmt.Sprintf("%s: the sandbox is fixed at profile %s", what, s.forced)
	if s.forcedBy != "" {
		msg += " by " + s.forcedBy
	}
	return errors.New(msg)
}

// Enabled reports whether wrapping is on.
func (s *Sandbox) Enabled() bool {
	return s != nil && s.onLocked()
}

func (s *Sandbox) onLocked() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.on
}

// Image is the docker image, or the default.
func (s *Sandbox) Image() string {
	if s == nil {
		return DefaultSandboxImage
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.image == "" {
		return DefaultSandboxImage
	}
	return s.image
}

// SetImage names the docker image used when wrapping. Empty restores the default.
func (s *Sandbox) SetImage(image string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.image = strings.TrimSpace(image)
	s.mu.Unlock()
}

// ProfileName is the active profile's name.
func (s *Sandbox) ProfileName() string {
	if s == nil {
		return DefaultProfile
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.profile == "" {
		return DefaultProfile
	}
	return s.profile
}

// Profile is the active profile.
func (s *Sandbox) Profile() Profile {
	p, _ := FindProfile(s.ProfileName())
	return p
}

// SetProfile selects a profile by name. It does not turn the sandbox on
// or check that the machine has a backend, so a project default can be
// recorded on a session that is not sandboxed; if the sandbox is already
// on, the next command runs under the new profile.
func (s *Sandbox) SetProfile(name string) error {
	if s == nil {
		return fmt.Errorf("no sandbox")
	}
	if _, ok := FindProfile(name); !ok {
		return fmt.Errorf("unknown sandbox profile %q (want %s)", name, strings.Join(ProfileNames(), ", "))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.forced != "" && name != s.forced {
		msg := fmt.Sprintf("sandbox profile %s: the sandbox is fixed at profile %s", name, s.forced)
		if s.forcedBy != "" {
			msg += " by " + s.forcedBy
		}
		return errors.New(msg)
	}
	s.profile = name
	return nil
}

// SetBackend forces a backend, or "auto" to pick one for the machine.
func (s *Sandbox) SetBackend(name string) error {
	if s == nil {
		return fmt.Errorf("no sandbox")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = BackendAuto
	}
	valid := false
	for _, b := range Backends {
		if b == name {
			valid = true
		}
	}
	if !valid {
		return fmt.Errorf("unknown sandbox backend %q (want %s)", name, strings.Join(Backends, ", "))
	}
	s.mu.Lock()
	if name == BackendAuto {
		s.backend = ""
	} else {
		s.backend = name
	}
	s.mu.Unlock()
	return nil
}

// Enable turns wrapping on after checking that the host can do it under
// the active profile.
func (s *Sandbox) Enable() error {
	if s == nil {
		return fmt.Errorf("no sandbox")
	}
	if _, err := s.usable(); err != nil {
		return err
	}
	s.mu.Lock()
	s.on = true
	s.mu.Unlock()
	return nil
}

// Disable turns wrapping off.
func (s *Sandbox) Disable() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.forced == "" {
		s.on = false
	}
	s.mu.Unlock()
}

// Backend reports the backend that would be used, "" when the machine
// has none.
func (s *Sandbox) Backend() string {
	b, _ := s.backendFor()
	return b
}

// backendFor resolves the backend: a forced one if it is available, else
// the best this machine has.
func (s *Sandbox) backendFor() (string, error) {
	forced := ""
	if s != nil {
		s.mu.Lock()
		forced = s.backend
		s.mu.Unlock()
	}
	switch forced {
	case BackendBwrap:
		if runtime.GOOS != "linux" {
			return "", fmt.Errorf("bubblewrap is Linux only")
		}
		if _, err := s.lookPath("bwrap"); err != nil {
			return "", fmt.Errorf("bwrap not found (install bubblewrap)")
		}
		if !s.bwrapUsable() {
			return "", fmt.Errorf("bwrap is installed but cannot run here: unprivileged user namespaces are off, or an AppArmor policy refuses them")
		}
		return BackendBwrap, nil
	case BackendDocker:
		if _, err := s.lookPath("docker"); err != nil {
			return "", fmt.Errorf("docker not found")
		}
		return BackendDocker, nil
	case BackendSeatbelt:
		if runtime.GOOS != "darwin" {
			return "", fmt.Errorf("sandbox-exec is macOS only")
		}
		if _, err := s.lookPath("sandbox-exec"); err != nil {
			return "", fmt.Errorf("sandbox-exec not found")
		}
		return BackendSeatbelt, nil
	case BackendLandlock:
		if runtime.GOOS != "linux" {
			return "", fmt.Errorf("landlock is a Linux facility; this machine is not Linux")
		}
		if s.landlockABI() < 1 {
			return "", fmt.Errorf("this kernel has no Landlock (needs Linux 5.13 or newer with the LSM enabled)")
		}
		return BackendLandlock, nil
	}
	switch runtime.GOOS {
	case "windows":
		return "", fmt.Errorf("sandbox is not supported on windows")
	case "darwin":
		if _, err := s.lookPath("sandbox-exec"); err != nil {
			return "", fmt.Errorf("sandbox-exec not found")
		}
		return BackendSeatbelt, nil
	case "linux":
		// bubblewrap first: it confines the command on this machine's
		// own toolchain, which is what the model was asked to work with,
		// and enforces more of a profile than Landlock can. Under an
		// egress policy it puts the command in a network namespace whose
		// only way out is a bridge to the proxy, which closes what
		// docker (the host's addresses) and Landlock (any host on the
		// proxy's port) leave open.
		if s.bwrapUsable() && (s.proxyPort(s.Profile()) == 0 || egressBridgeSupported()) {
			return BackendBwrap, nil
		}
		if _, err := s.lookPath("docker"); err == nil {
			return BackendDocker, nil
		}
		if s.landlockABI() >= 1 {
			return BackendLandlock, nil
		}
		return "", fmt.Errorf("no sandbox backend: bubblewrap and docker are not installed and this kernel has no Landlock")
	default:
		if _, err := s.lookPath("docker"); err != nil {
			return "", fmt.Errorf("docker not found (needed to sandbox bash)")
		}
		return BackendDocker, nil
	}
}

// usable resolves the backend and checks it can carry the active profile
// at all. Landlock cannot take a network away before ABI 4, so strict —
// whose whole point is that there is no network — is refused there
// rather than quietly running with one.
func (s *Sandbox) usable() (string, error) {
	backend, err := s.backendFor()
	if err != nil {
		return "", err
	}
	p := s.Profile()
	proxy := s.proxyPort(p) > 0
	if backend == BackendBwrap && proxy && !egressBridgeSupported() {
		return "", fmt.Errorf("bubblewrap cannot narrow the network to the egress proxy on this build; use the docker or landlock backend, or set egress.default to allow")
	}
	if backend == BackendLandlock && !p.Network && s.landlockABI() < 4 {
		return "", fmt.Errorf("the %s profile needs the network taken away, and Landlock ABI %d cannot do that (needs ABI 4, Linux 6.7); install docker, or use the workspace profile", p.Name, s.landlockABI())
	}
	if backend == BackendLandlock && (!p.Network || proxy) && !landlockSocketFilter {
		if proxy {
			return "", fmt.Errorf("the egress policy needs unix sockets and UDP taken away, and this architecture has no socket filter for Landlock to pair with; install docker, or set egress.default to allow")
		}
		return "", fmt.Errorf("the %s profile needs unix sockets and UDP taken away, and this architecture has no socket filter for Landlock to pair with; install docker, or use the workspace profile", p.Name)
	}
	return backend, nil
}

// Notes lists what the active backend cannot enforce about the active
// profile, so the status line admits it instead of implying a stronger
// box than the one the command runs in.
func (s *Sandbox) Notes() []string {
	backend, err := s.backendFor()
	if err != nil {
		return []string{err.Error()}
	}
	p := s.Profile()
	var out []string
	switch backend {
	case BackendDocker:
		if wild := wildcardGlobs(p.DenyGlobs); len(wild) > 0 {
			out = append(out, "docker cannot mount a wildcard deny glob ("+strings.Join(wild, ", ")+"); those writes are refused by command analysis instead")
		}
		out = append(out, "the command runs in the "+s.Image()+" image, not on your machine's toolchain")
	case BackendBwrap:
		if wild := wildcardGlobs(p.DenyGlobs); len(wild) > 0 {
			out = append(out, "bubblewrap cannot mount a wildcard deny glob ("+strings.Join(wild, ", ")+"), nor a protected directory that does not exist yet; those writes are refused by command analysis instead")
		}
		if !p.Network {
			out = append(out, "unix sockets on the filesystem stay reachable without a network")
		}
	case BackendSeatbelt:
		out = append(out, "seatbelt allows a command to read anything outside the write roots")
	case BackendLandlock:
		if len(p.DenyGlobs) > 0 {
			out = append(out, "Landlock has allow rules only, so deny globs are refused by command analysis, not by the kernel")
		}
		if !p.Network {
			out = append(out, "no network is Landlock for TCP plus a seccomp filter that refuses every other socket (unix, UDP, raw) but netlink; docker's --network=none is the same no from a separate network namespace")
		}
	}
	return out
}

// Status is the one-line /sandbox report.
func (s *Sandbox) Status() string {
	if s == nil || !s.Enabled() {
		return "sandbox off · bash runs on the workspace host · /sandbox " + strings.Join(ProfileNames(), "|") + " turns it on"
	}
	p := s.Profile()
	backend := s.Backend()
	if backend == BackendDocker {
		backend += " (" + s.Image() + ")"
	}
	net := "network allowed"
	if !p.Network {
		net = "no network"
	}
	line := "sandbox on · profile " + p.Name + " · backend " + backend + " · " + net + " · " + p.Description
	if notes := s.Notes(); len(notes) > 0 {
		line += "\nnote: " + strings.Join(notes, "; ")
	}
	return line
}

// SetSandbox applies a /sandbox argument and returns the status line to
// show. The argument is one of: "" or "status" (report), "on" (the
// default profile), "off", a profile name, a backend name, or a docker
// image. sb may be nil, meaning the session has none.
func SetSandbox(sb *Sandbox, arg string) (string, error) {
	if sb == nil {
		return "", fmt.Errorf("this session has no sandbox")
	}
	arg = strings.TrimSpace(arg)
	switch arg {
	case "", "status":
		return sb.Status(), nil
	case "off":
		if err := sb.forcedErr("sandbox off"); err != nil {
			return "", err
		}
		sb.Disable()
		return sb.Status(), nil
	case "on":
		if err := sb.SetProfile(DefaultProfile); err != nil {
			return "", err
		}
		return enable(sb)
	}
	if _, ok := FindProfile(arg); ok {
		if err := sb.SetProfile(arg); err != nil {
			return "", err
		}
		return enable(sb)
	}
	for _, b := range Backends {
		if b == arg {
			if err := sb.SetBackend(arg); err != nil {
				return "", fmt.Errorf("sandbox: %w", err)
			}
			if !sb.Enabled() {
				return enable(sb)
			}
			return sb.Status(), nil
		}
	}
	if looksLikeProfile(arg) {
		return "", fmt.Errorf("unknown sandbox profile %q (want %s, a backend: %s, or a docker image)", arg, strings.Join(ProfileNames(), ", "), strings.Join(Backends, ", "))
	}
	sb.SetImage(arg)
	if err := sb.SetBackend(BackendDocker); err != nil {
		return "", fmt.Errorf("sandbox: %w", err)
	}
	if !sb.Enabled() {
		return enable(sb)
	}
	return sb.Status(), nil
}

// looksLikeProfile catches a mistyped profile name before it is taken
// for a docker image: an image reference carries a tag, a registry or a
// repository path, so a bare lowercase word with a dash in it is far
// likelier to be a fumbled "read-only" than an image.
func looksLikeProfile(arg string) bool {
	if strings.ContainsAny(arg, ":/@.") {
		return false
	}
	return strings.Contains(arg, "-")
}

func enable(sb *Sandbox) (string, error) {
	if err := sb.Enable(); err != nil {
		return "", fmt.Errorf("sandbox: %w", err)
	}
	return sb.Status(), nil
}

func (s *Sandbox) lookPath(name string) (string, error) {
	if s != nil && s.look != nil {
		return s.look(name)
	}
	return exec.LookPath(name)
}

// landlockABI reports the kernel's Landlock ABI version, 0 for none.
func (s *Sandbox) landlockABI() int {
	if s != nil && s.abi != nil {
		return s.abi()
	}
	return landlockABI()
}

// Command wraps command so it runs inside the sandbox, cwd root.
func (s *Sandbox) Command(ctx context.Context, root, command string) (*exec.Cmd, error) {
	return s.CommandIn(ctx, root, nil, command)
}

// CommandIn is Command with the session's extra directories, which the
// profile's write roots include.
func (s *Sandbox) CommandIn(ctx context.Context, root string, extra []string, command string) (*exec.Cmd, error) {
	if s == nil || !s.Enabled() {
		return nil, fmt.Errorf("sandbox is off")
	}
	backend, err := s.usable()
	if err != nil {
		return nil, err
	}
	p := s.Profile()
	// The deny globs come first, for every backend: a refusal the model
	// can read beats a permission error from inside a container, and it
	// is the only enforcement Landlock can have at all.
	if err := p.CheckCommand(command, root, extra); err != nil {
		return nil, err
	}
	switch backend {
	case BackendSeatbelt:
		return s.seatbelt(ctx, p, root, extra, command)
	case BackendLandlock:
		return s.landlock(ctx, p, root, extra, command)
	case BackendBwrap:
		return s.bwrap(ctx, p, root, extra, command)
	default:
		return s.docker(ctx, p, root, extra, command)
	}
}
