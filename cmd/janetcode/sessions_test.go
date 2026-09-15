package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
)

// TestSessionListingShowsForkLineage: --sessions lists a fork indented
// under the session it was copied from, and a fork whose parent is not in
// the listing at top level, saying where its parent went.
func TestSessionListingShowsForkLineage(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	work := t.TempDir()
	t.Chdir(work)

	dir, err := session.Dir()
	if err != nil {
		t.Fatal(err)
	}
	start := func(title string) *session.Session {
		s, err := session.New(dir, work, "test", "test-model")
		if err != nil {
			t.Fatal(err)
		}
		msg := llm.Message{Role: llm.RoleUser, Blocks: []llm.Block{{Text: title}}}
		if _, err := s.Append(session.Event{Type: session.TypeUserMessage, Message: &msg}); err != nil {
			t.Fatal(err)
		}
		if err := s.SetTitle(title); err != nil {
			t.Fatal(err)
		}
		return s
	}
	parent := start("the original")
	child, err := session.Fork(dir, parent, "the fork")
	if err != nil {
		t.Fatal(err)
	}
	// A fork whose parent is not on disk here: the parent was deleted, or
	// the fork was made in another directory.
	orphan := start("a stray fork")
	orphan.Meta.ForkOf = "20000101-000000-dead"
	if err := orphan.SetTitle("a stray fork"); err != nil {
		t.Fatal(err)
	}

	stdout, _ := captureStd(t)
	if err := printSessions(); err != nil {
		t.Fatal(err)
	}
	out := stdout()
	var forkLine string
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, child.Meta.ID) {
			forkLine = l
		}
	}
	if !strings.HasPrefix(forkLine, "└ ") {
		t.Fatalf("the fork is not indented under its parent:\n%s", out)
	}
	pi := strings.Index(out, parent.Meta.ID)
	ci := strings.Index(out, child.Meta.ID)
	if pi < 0 || ci < pi {
		t.Fatalf("the fork does not follow its parent:\n%s", out)
	}
	if !strings.Contains(out, "20000101-000000-dead, not listed here") {
		t.Fatalf("the stray fork does not say where its parent is:\n%s", out)
	}
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, orphan.Meta.ID) && strings.HasPrefix(l, " ") {
			t.Fatalf("a fork with no parent here should list at top level:\n%s", out)
		}
	}
	if !strings.Contains(out, "indented sessions are forks") {
		t.Fatalf("the listing does not explain the indent:\n%s", out)
	}
}
