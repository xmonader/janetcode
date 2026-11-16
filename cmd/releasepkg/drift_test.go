package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/shell"
)

// These tests are the drift check for the release documentation: they run
// in `go test ./...` and so in CI, and they fail when
// docs/manual/security.md says something the implementation no longer
// does, or the implementation grows something the page does not say.

const root = "../.."

func read(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestSecurityDocNamesEveryGateCommand(t *testing.T) {
	doc, mk := read(t, "docs/manual/security.md"), read(t, "Makefile")
	targets := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^([A-Za-z0-9_.-]+):`).FindAllStringSubmatch(mk, -1) {
		targets[m[1]] = true
	}
	for _, g := range Gates {
		if !strings.Contains(doc, "`"+g.Command+"`") {
			t.Errorf("gate %q: the security manual does not list its exact command `%s`", g.Name, g.Command)
		}
		for _, part := range strings.Split(g.Command, "&&") {
			f := strings.Fields(part)
			if len(f) > 0 && f[0] == "make" {
				for _, tgt := range f[1:] {
					if !targets[tgt] {
						t.Errorf("gate %q runs `make %s`, which the Makefile does not define", g.Name, tgt)
					}
				}
			}
		}
	}
	for _, tgt := range []string{"release-evidence", "verify-dist", "repro-check", "release-sign", "release-gates", "release-meta"} {
		if !targets[tgt] || !strings.Contains(doc, "make "+tgt) {
			t.Errorf("make %s must exist and be documented", tgt)
		}
	}
}

func TestSecurityDocMatchesTheSandbox(t *testing.T) {
	doc := read(t, "docs/manual/security.md")
	for _, b := range shell.Backends {
		if b == shell.BackendAuto {
			continue
		}
		if !strings.Contains(doc, "`"+b+"`") {
			t.Errorf("the security manual does not describe the %q sandbox backend", b)
		}
	}
	for _, p := range shell.Profiles {
		if !strings.Contains(doc, "`"+p.Name+"`") {
			t.Errorf("the security manual does not describe the %q sandbox profile", p.Name)
		}
	}
}

func TestSecurityDocToolchainMatchesGoMod(t *testing.T) {
	m := regexp.MustCompile(`(?m)^go (\d+\.\d+(?:\.\d+)?)$`).FindStringSubmatch(read(t, "go.mod"))
	if m == nil {
		t.Fatal("no go directive in go.mod")
	}
	if !strings.Contains(read(t, "docs/manual/security.md"), "Go "+m[1]) {
		t.Errorf("the security manual does not name the toolchain Go %s that go.mod requires", m[1])
	}
}

func TestSecurityDocHasTheRequiredLimitations(t *testing.T) {
	doc := read(t, "docs/manual/security.md")
	for _, h := range []string{"## Threat model", "## Known limitations", "### Windows safety classification", "### Remote workspace trust", "### Provider-specific guarantees", "## Telemetry and data retention", "## Verify a release on a clean machine"} {
		if !strings.Contains(doc, "\n"+h+"\n") {
			t.Errorf("the security manual lacks the section %q", h)
		}
	}
}

// Every host the program can talk to is named in the manual's endpoint
// table, so the privacy statement cannot go stale by someone adding a
// call.
func TestSecurityDocListsEveryOutboundHost(t *testing.T) {
	doc := read(t, "docs/manual/security.md")
	urlRe := regexp.MustCompile(`"https?://([A-Za-z0-9.-]+)`)
	found := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "testdata", "worktrees", "dist", "bin", "site", "vscode", "eval", "evals":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range urlRe.FindAllStringSubmatch(string(data), -1) {
			if h := m[1]; !placeholderHost(h) {
				found[h] = path
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var missing []string
	for h, path := range found {
		if !strings.Contains(doc, h) {
			missing = append(missing, h+" ("+strings.TrimPrefix(path, root+"/")+")")
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("hosts in the source that the security manual's endpoint table does not name:\n  %s", strings.Join(missing, "\n  "))
	}
}

// placeholderHost reports hosts that are loopback, reserved for examples
// (RFC 2606) or single-label, none of which is a real destination.
func placeholderHost(h string) bool {
	switch h {
	case "localhost", "127.0.0.1", "169.254.169.254", "example.com", "example.org", "example.net", "x.com":
		return true
	}
	if !strings.Contains(h, ".") {
		return true
	}
	for _, s := range []string{".example", ".test", ".invalid", ".example.com", ".example.org", ".localhost", ".internal", ".corp"} {
		if strings.HasSuffix(h, s) {
			return true
		}
	}
	return false
}

func TestPlaceholderHost(t *testing.T) {
	for h, want := range map[string]bool{
		"example.com": true, "gw.example": true, "mcp.example.test": true, "localhost": true, "h": true,
		"api.openai.com": false, "raw.githubusercontent.com": false, "go.dev.evil.example": true, "pkg.go.dev": false,
	} {
		if placeholderHost(h) != want {
			t.Errorf("placeholderHost(%q) = %v", h, !want)
		}
	}
}
