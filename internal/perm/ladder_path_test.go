//go:build !windows

package perm

import (
	"os"
	"path/filepath"
	"testing"
)

// The r prompt offers a typed rung for a file tool: write:docs/* judges
// the path after symlinks are resolved, so a link inside docs that points
// out of it is not covered, where the bare docs/* rung covers it.
func TestPathLadderOffersATypedRung(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "docs", "out")); err != nil {
		t.Fatal(err)
	}
	var typed Rule
	for _, r := range SuggestLadder("write_file", "docs/a.md") {
		if r.String() == "write_file(write:docs/*)" {
			typed = r
		}
	}
	if typed.IsZero() {
		t.Fatalf("no typed rung in %v", SuggestLadder("write_file", "docs/a.md"))
	}
	inside := toolCall(root, "write_file", map[string]any{"path": "docs/a.md", "content": "x"})
	escape := toolCall(root, "write_file", map[string]any{"path": "docs/out/x.md", "content": "x"})
	rs := Rules{}
	rs.Add(Allowed, typed)
	if v := verdictOf(rs, inside); v != Allowed {
		t.Errorf("typed rung does not allow the call it came from: %v", v)
	}
	if v := verdictOf(rs, escape); v == Allowed {
		t.Errorf("typed rung allows a write through a link out of docs")
	}
	bare := Rules{}
	bare.Add(Allowed, Rule{Tool: "write_file", Pattern: "docs/*"})
	if v := verdictOf(bare, escape); v != Allowed {
		t.Logf("the bare rung also refuses the link (%v): the typed rung is still the resolved one", v)
	}
}
