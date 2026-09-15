package shell

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// SeatbeltPolicy is the sandbox-exec profile text for one janetcode
// profile: reads stay broad, the write roots are allowed by subpath, the
// deny globs are denied after them (a later rule wins in seatbelt), and
// a profile with no network denies network*.
//
// It is exported so a test can state the policy per profile, and so
// /sandbox could one day show it.
func SeatbeltPolicy(p Profile, root string, extra []string) string {
	var b strings.Builder
	b.WriteString("(version 1)\n(deny default)\n(allow file-read*)\n")
	roots := p.WritableRoots(root, extra, os.TempDir())
	for _, r := range roots {
		b.WriteString(`(allow file-write* (subpath "` + r + `"))` + "\n")
	}
	if len(roots) > 0 {
		// The macOS temp dir is reached through three names.
		if isTempRoot(roots) {
			b.WriteString(`(allow file-write* (subpath "/tmp") (subpath "/private/tmp") (subpath "/var/tmp"))` + "\n")
		}
	}
	for _, g := range p.DenyGlobs {
		if head := literalDenyPrefix(g); head != "" {
			for _, r := range roots {
				b.WriteString(`(deny file-write* (subpath "` + filepath.Join(r, head) + `"))` + "\n")
			}
			continue
		}
		if re := seatbeltDenyRegex(g); re != "" {
			b.WriteString(`(deny file-write* (regex #"` + re + `"))` + "\n")
		}
	}
	b.WriteString(`(allow file-ioctl (regex #"^/dev/"))
(allow file-write-data (regex #"^/dev/"))
(allow process-exec*)
(allow process-fork)
(allow signal)
(allow sysctl-read)
(allow mach-lookup)
`)
	if p.Network {
		b.WriteString("(allow network*)\n")
	} else {
		b.WriteString("(deny network*)\n")
	}
	b.WriteString("(allow ipc-posix-shm)\n")
	return b.String()
}

// isTempRoot reports whether the temp dir is among the write roots.
func isTempRoot(roots []string) bool {
	tmp := filepath.Clean(os.TempDir())
	for _, r := range roots {
		if r == tmp {
			return true
		}
	}
	return false
}

// seatbeltDenyRegex turns a "**/"-anchored deny glob into the regex
// seatbelt matches absolute paths with. "**/id_rsa" becomes "/id_rsa$",
// "**/.ssh/**" becomes "/\.ssh/". A glob of any other shape returns "",
// and Profile.CheckCommand is what refuses it.
func seatbeltDenyRegex(glob string) string {
	if !strings.HasPrefix(glob, "**/") {
		return ""
	}
	tail := strings.TrimPrefix(glob, "**/")
	dir := false
	if strings.HasSuffix(tail, "/**") {
		tail, dir = strings.TrimSuffix(tail, "/**"), true
	}
	if strings.ContainsAny(tail, "*?") {
		return ""
	}
	out := "/" + regexEscape(tail)
	if dir {
		return out + "/"
	}
	return out + "$"
}

// regexEscape quotes the characters a path may carry that mean
// something in seatbelt's regex syntax.
func regexEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(`.+*?()|[]{}^$\`, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

func (s *Sandbox) seatbelt(ctx context.Context, p Profile, root string, extra []string, command string) (*exec.Cmd, error) {
	bin, err := s.lookPath("sandbox-exec")
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, bin, "-p", SeatbeltPolicy(p, root, extra), "sh", "-c", command)
	cmd.Dir = root
	cmd.Env = ScrubEnv(os.Environ())
	SetProcessGroup(cmd)
	return cmd, nil
}
