package shell

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestProfileTable states the catalogue: what each named profile lets a
// command write, what it never lets it write, and whether it has a
// network. A profile that changes has to change here too, which is the
// point — this is the table the docs and the status line describe.
func TestProfileTable(t *testing.T) {
	cases := []struct {
		name       string
		writeRoots []string
		network    bool
		denies     []string // paths under /proj that must be refused
		allows     []string // paths under /proj that must not be
	}{
		{
			name:       "workspace",
			writeRoots: []string{WriteRootToken, WriteExtraToken, WriteTempToken},
			network:    true,
			denies:     []string{"/proj/.janetcode/state.json", "/proj/keys/id_rsa", "/home/u/.ssh/config", "/proj/.git/hooks/pre-commit"},
			allows:     []string{"/proj/main.go", "/proj/.git/config", "/proj/.git/objects/ab/cdef", "/proj/.git/refs/heads/main"},
		},
		{
			name:       "read-only",
			writeRoots: nil,
			network:    true,
			allows:     []string{"/proj/main.go"},
		},
		{
			name:       "strict",
			writeRoots: []string{WriteRootToken, WriteExtraToken, WriteTempToken},
			network:    false,
			denies:     []string{"/proj/.git/config", "/proj/.janetcode/state.json", "/proj/keys/id_rsa"},
			allows:     []string{"/proj/main.go"},
		},
	}
	if len(cases) != len(Profiles) {
		t.Fatalf("the catalogue has %d profiles, the table %d", len(Profiles), len(cases))
	}
	for _, c := range cases {
		p, ok := FindProfile(c.name)
		if !ok {
			t.Fatalf("no profile %q", c.name)
		}
		if strings.Join(p.WriteRoots, ",") != strings.Join(c.writeRoots, ",") {
			t.Errorf("%s write roots = %v, want %v", c.name, p.WriteRoots, c.writeRoots)
		}
		if p.Network != c.network {
			t.Errorf("%s network = %v, want %v", c.name, p.Network, c.network)
		}
		roots := p.WritableRoots("/proj", []string{"/extra"}, "/tmp")
		for _, path := range c.denies {
			if _, denied := p.DeniedWrite(path, roots); !denied {
				t.Errorf("%s: %s should be denied", c.name, path)
			}
		}
		for _, path := range c.allows {
			if g, denied := p.DeniedWrite(path, roots); denied {
				t.Errorf("%s: %s denied by %q, want allowed", c.name, path, g)
			}
		}
		if p.Description == "" {
			t.Errorf("%s has no description", c.name)
		}
	}
	if _, ok := FindProfile("nonesuch"); ok {
		t.Error("an unknown profile resolved")
	}
	if _, ok := FindProfile(DefaultProfile); !ok {
		t.Errorf("the default profile %q is not in the catalogue", DefaultProfile)
	}
}

func TestWritableRootsResolveTokens(t *testing.T) {
	p := Profile{WriteRoots: []string{WriteRootToken, WriteExtraToken, WriteTempToken}}
	got := p.WritableRoots("/proj", []string{"/extra", "/proj"}, "/tmp")
	want := []string{"/extra", "/proj", "/tmp"} // sorted, deduplicated
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("roots = %v, want %v", got, want)
	}
	if roots := (Profile{}).WritableRoots("/proj", nil, "/tmp"); len(roots) != 0 {
		t.Fatalf("a profile with no write roots resolved %v", roots)
	}
}

func TestGlobMatching(t *testing.T) {
	p := Profile{DenyGlobs: []string{".git/**", "**/id_rsa", "**/.ssh/**", "build/*.lock"}}
	roots := []string{"/proj"}
	deny := []string{"/proj/.git", "/proj/.git/config", "/proj/.git/a/b/c", "/proj/id_rsa", "/deep/down/id_rsa", "/proj/.ssh/known_hosts", "/proj/build/x.lock"}
	allow := []string{"/proj/git/config", "/proj/id_rsa.pub", "/proj/build/sub/x.lock", "/other/.git/config"}
	for _, path := range deny {
		if _, ok := p.DeniedWrite(path, roots); !ok {
			t.Errorf("%s should match a deny glob", path)
		}
	}
	for _, path := range allow {
		if g, ok := p.DeniedWrite(path, roots); ok {
			t.Errorf("%s matched %q, want no match", path, g)
		}
	}
}

// TestCheckCommandRefusesDenyGlob is the enforcement no backend can do:
// the write targets of the command line itself are read with
// WriteTargets and graded against the profile before anything runs.
func TestCheckCommandRefusesDenyGlob(t *testing.T) {
	p, _ := FindProfile("strict")
	root := "/proj"
	refused := []string{
		"rm -rf .git",
		"echo x > .git/config",
		"cp /etc/passwd .janetcode/notes",
		"sudo tee ~/.ssh/authorized_keys",
	}
	for _, cmd := range refused {
		if err := p.CheckCommand(cmd, root, nil); err == nil {
			t.Errorf("%q was allowed, want refused", cmd)
		}
	}
	for _, cmd := range []string{"go test ./...", "rm -rf build", "echo hi > out.txt"} {
		if err := p.CheckCommand(cmd, root, nil); err != nil {
			t.Errorf("%q refused: %v", cmd, err)
		}
	}
	// An extra dir is a write root of its own, and the globs apply there too.
	extra := filepath.Join(string(filepath.Separator), "extra")
	if err := p.CheckCommand("rm -rf "+filepath.Join(extra, ".git"), root, []string{extra}); err == nil {
		t.Error("a deny glob should apply inside an extra dir")
	}
	// read-only has no deny globs, so nothing is refused here; the
	// backend is what stops the write.
	ro, _ := FindProfile("read-only")
	if err := ro.CheckCommand("rm -rf .git", root, nil); err != nil {
		t.Errorf("read-only refused a write it leaves to the backend: %v", err)
	}
}
