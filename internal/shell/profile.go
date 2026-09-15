package shell

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Write-root tokens. A profile names the roots it makes writable as
// tokens rather than paths, because the paths are only known when a
// command is about to run: the project root, the directories the session
// granted with /add-dir, and the machine's temp dir.
const (
	// WriteRootToken is the project root the command runs in.
	WriteRootToken = "<root>"
	// WriteExtraToken is every directory added with /add-dir.
	WriteExtraToken = "<extra-dirs>"
	// WriteTempToken is the machine's temp dir (/tmp and friends). It is
	// scratch space, not the user's data, and a shell that cannot write
	// there fails at things as ordinary as a here-document.
	WriteTempToken = "<temp>"
)

// Profile is a named sandbox policy: what a command may write, what it
// may never write, and whether it has a network. It is pure data, so a
// backend reads a profile rather than special-casing its name, and a
// test can state the whole table in one place.
type Profile struct {
	// Name is what /sandbox and the sandbox config key accept.
	Name string
	// Description is the one line shown by /sandbox and the docs.
	Description string
	// WriteRoots are the write-root tokens above. Empty means nothing is
	// writable at all.
	WriteRoots []string
	// DenyGlobs are paths that stay unwritable even inside a write root,
	// as globs where "**" spans any number of path segments. A glob that
	// starts with "**/" matches anywhere; any other glob is matched
	// against the path relative to each write root.
	DenyGlobs []string
	// Network reports whether the command may reach the network.
	Network bool
}

// secretGlobs are the paths no profile lets a sandboxed command write:
// the harness's own state, and the private keys a command has no reason
// to rewrite.
var secretGlobs = []string{
	".janetcode/**",
	"**/id_rsa",
	"**/id_ed25519",
	"**/.ssh/**",
	"**/.aws/credentials",
}

// Profiles is the catalogue, in the order /sandbox lists them. workspace
// is what "on" means, so it comes first.
var Profiles = []Profile{
	{
		Name:        "workspace",
		Description: "writes confined to the project root, the session's extra dirs and the temp dir; reads broad; network allowed",
		WriteRoots:  []string{WriteRootToken, WriteExtraToken, WriteTempToken},
		DenyGlobs:   secretGlobs,
		Network:     true,
	},
	{
		Name:        "read-only",
		Description: "no writes anywhere; reads broad; network allowed",
		WriteRoots:  nil,
		DenyGlobs:   nil, // nothing is writable, so there is nothing to carve out
		Network:     true,
	},
	{
		Name:        "strict",
		Description: "writes confined to the project root, the session's extra dirs and the temp dir, with git history off limits; no network",
		WriteRoots:  []string{WriteRootToken, WriteExtraToken, WriteTempToken},
		DenyGlobs:   append([]string{".git/**"}, secretGlobs...),
		Network:     false,
	},
}

// DefaultProfile is the profile "on" selects and the one a sandbox that
// was never told otherwise uses.
const DefaultProfile = "workspace"

// FindProfile returns the named profile.
func FindProfile(name string) (Profile, bool) {
	for _, p := range Profiles {
		if p.Name == strings.TrimSpace(name) {
			return p, true
		}
	}
	return Profile{}, false
}

// ProfileNames lists the catalogue for a message or a help line.
func ProfileNames() []string {
	names := make([]string, len(Profiles))
	for i, p := range Profiles {
		names[i] = p.Name
	}
	return names
}

// WritableRoots resolves the profile's tokens into absolute directories,
// given the project root and the session's extra dirs. The result is
// deduplicated and sorted so a backend builds the same argv every time.
func (p Profile) WritableRoots(root string, extra []string, tempDir string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(d string) {
		if d == "" {
			return
		}
		d = filepath.Clean(d)
		if seen[d] {
			return
		}
		seen[d] = true
		out = append(out, d)
	}
	for _, tok := range p.WriteRoots {
		switch tok {
		case WriteRootToken:
			add(root)
		case WriteExtraToken:
			for _, d := range extra {
				add(d)
			}
		case WriteTempToken:
			add(tempDir)
		default:
			add(tok) // a literal path, for a profile that names one
		}
	}
	sort.Strings(out)
	return out
}

// DeniedWrite reports the deny glob that forbids writing path, if any.
// path is absolute; roots are the resolved write roots the globs without
// a "**/" prefix are relative to.
func (p Profile) DeniedWrite(path string, roots []string) (string, bool) {
	if path == "" {
		return "", false
	}
	path = filepath.Clean(path)
	segs := splitPath(path)
	for _, g := range p.DenyGlobs {
		pat := splitPath(g)
		switch {
		case strings.HasPrefix(g, "/"):
			if globMatch(pat, segs) {
				return g, true
			}
		case strings.HasPrefix(g, "**/"):
			if globMatch(pat, segs) {
				return g, true
			}
		default:
			for _, r := range roots {
				rel, err := filepath.Rel(filepath.Clean(r), path)
				if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
					continue
				}
				if globMatch(pat, splitPath(rel)) {
					return g, true
				}
			}
		}
	}
	return "", false
}

// splitPath splits a path into its segments, dropping the leading empty
// segment an absolute path produces.
func splitPath(p string) []string {
	p = filepath.ToSlash(p)
	parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
	out := parts[:0]
	for _, s := range parts {
		if s != "" && s != "." {
			out = append(out, s)
		}
	}
	return out
}

// globMatch matches segmented glob pattern against a segmented path.
// "**" spans any number of segments, including none; every other segment
// is matched with filepath.Match, so "*" and "?" work within a segment.
func globMatch(pat, s []string) bool {
	if len(pat) == 0 {
		return len(s) == 0
	}
	if pat[0] == "**" {
		for i := 0; i <= len(s); i++ {
			if globMatch(pat[1:], s[i:]) {
				return true
			}
		}
		return false
	}
	if len(s) == 0 {
		return false
	}
	ok, err := filepath.Match(pat[0], s[0])
	if err != nil || !ok {
		return false
	}
	return globMatch(pat[1:], s[1:])
}

// CheckCommand refuses a command whose write targets the profile's deny
// globs forbid, before it runs. Backends enforce what the OS lets them
// (a read-only mount, a seatbelt deny rule); this is the backstop for
// the rest — Landlock has no deny rules at all, and no backend can
// express a wildcard glob like "**/id_rsa". It reads the command with
// WriteTargets, the same static analysis the accept-tests lock uses, so
// a denied write is graded exactly the way an edit_file to the same path
// would be.
func (p Profile) CheckCommand(command, root string, extra []string) error {
	if len(p.DenyGlobs) == 0 {
		return nil
	}
	roots := append(p.WritableRoots(root, extra, os.TempDir()), filepath.Clean(root))
	for _, t := range WriteTargets(command, root) {
		if g, ok := p.DeniedWrite(t, roots); ok {
			return fmt.Errorf("refused: the %s sandbox profile never lets a command write %s (deny glob %q)", p.Name, t, g)
		}
	}
	return nil
}
