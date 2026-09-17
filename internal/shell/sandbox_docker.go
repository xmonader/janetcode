package shell

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// literalDenyPrefix returns the directory a deny glob names when the
// glob is a literal path with a "/**" tail — the only shape a backend
// can express as a mount or a subpath rule. It is relative to a write
// root; "" means the glob needs a wildcard match instead.
func literalDenyPrefix(glob string) string {
	if !strings.HasSuffix(glob, "/**") {
		return ""
	}
	head := strings.TrimSuffix(glob, "/**")
	if head == "" || strings.ContainsAny(head, "*?") {
		return ""
	}
	return head
}

// wildcardGlobs are the deny globs no mount or subpath rule can express.
func wildcardGlobs(globs []string) []string {
	var out []string
	for _, g := range globs {
		if literalDenyPrefix(g) == "" {
			out = append(out, g)
		}
	}
	return out
}

// docker builds the container that carries the profile: the write roots
// are bind-mounted read-write and everything else the command can see is
// read-only, deny globs with a literal prefix are re-mounted read-only on
// top, and a profile with no network gets --network=none.
func (s *Sandbox) docker(ctx context.Context, p Profile, root string, extra []string, command string) (*exec.Cmd, error) {
	bin, err := s.lookPath("docker")
	if err != nil {
		return nil, err
	}
	roots := p.WritableRoots(root, extra, os.TempDir())
	writable := map[string]bool{}
	for _, d := range roots {
		writable[d] = true
	}
	args := []string{"run", "--rm", "-i"}
	if p.Network {
		args = append(args, "--network", "bridge")
	} else {
		args = append(args, "--network", "none")
	}
	if len(roots) == 0 {
		// Nothing is writable, the container's own filesystem included.
		args = append(args, "--read-only")
	}
	for _, d := range append([]string{root}, extra...) {
		d = filepath.Clean(d)
		mount := d + ":" + d
		if !writable[d] {
			mount += ":ro"
		}
		args = append(args, "-v", mount)
	}
	// Deny globs that name a directory are mounted read-only over the
	// read-write root they live in. A path that is not there yet is
	// skipped: docker would create it, owned by root, which is worse
	// than leaving it to the command analysis in Profile.CheckCommand.
	for _, g := range p.DenyGlobs {
		head := literalDenyPrefix(g)
		if head == "" {
			continue
		}
		for _, r := range roots {
			path := filepath.Join(r, head)
			if _, err := os.Stat(path); err != nil {
				continue
			}
			args = append(args, "-v", path+":"+path+":ro")
		}
	}
	if writable[filepath.Clean(os.TempDir())] {
		args = append(args, "--tmpfs", "/tmp")
	}
	args = append(args, "-w", root)
	if uid := sandboxUser(); uid != "" {
		args = append(args, "-u", uid)
	}
	args = append(args, s.Image(), "sh", "-c", command)
	cmd := exec.CommandContext(ctx, bin, args...)
	// The container process must not inherit the agent's credentials: a
	// model command inside the sandbox could echo $ANTHROPIC_API_KEY into
	// the writable workspace mount (the landlock and seatbelt backends
	// already scrub; docker did not).
	cmd.Env = ScrubEnv(os.Environ())
	SetProcessGroup(cmd)
	return cmd, nil
}

func sandboxUser() string {
	if runtime.GOOS == "windows" {
		return ""
	}
	uid, gid := os.Getuid(), os.Getgid()
	if uid < 0 || gid < 0 {
		return ""
	}
	return fmt.Sprintf("%d:%d", uid, gid)
}
