package session

import (
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

// TestAcceptMetaRoundTripsThroughOpen is the persistence half of "resume
// restores the lock": /accept's state is carried on Meta, so an agent
// rebuilt from a reopened session sees the same locked files, hashes and
// command an earlier process approved.
func TestAcceptMetaRoundTripsThroughOpen(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir, "/proj", "anthropic", "m")
	if err != nil {
		t.Fatal(err)
	}
	put(t, s, llm.TextMessage(llm.RoleUser, "add foo"), llm.TextMessage(llm.RoleAssistant, "done"))
	meta := &AcceptMeta{
		Phase:   AcceptLocked,
		Request: "add a foo endpoint",
		Root:    "/proj",
		Files:   []string{"/proj/foo_test.go"},
		Hashes:  map[string]string{"/proj/foo_test.go": "deadbeef"},
		Command: "go test ./...",
	}
	if err := s.SetAccept(meta); err != nil {
		t.Fatal(err)
	}
	re, back, err := Open(dir, s.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(back) != 2 {
		t.Fatalf("messages after accept rewrite: %d", len(back))
	}
	got := re.Meta.Accept
	if got == nil || got.Phase != AcceptLocked || got.Request != meta.Request || got.Command != meta.Command {
		t.Fatalf("accept meta did not round-trip: %+v", got)
	}
	if len(got.Files) != 1 || got.Files[0] != "/proj/foo_test.go" {
		t.Fatalf("files did not round-trip: %+v", got.Files)
	}
	if got.Hashes["/proj/foo_test.go"] != "deadbeef" {
		t.Fatalf("hashes did not round-trip: %+v", got.Hashes)
	}
	// Clearing it (off) must round-trip too, not just leave the old value.
	if err := re.SetAccept(nil); err != nil {
		t.Fatal(err)
	}
	re2, _, err := Open(dir, s.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if re2.Meta.Accept != nil {
		t.Fatalf("accept meta after /accept off: %+v", re2.Meta.Accept)
	}
}
