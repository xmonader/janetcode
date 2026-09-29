package webui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/trust"
)

func TestWebTrustAsksAndSets(t *testing.T) {
	s, root := planServer(t)
	if err := os.MkdirAll(filepath.Join(root, ".janetcode", "skills", "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".janetcode", "skills", "x", "SKILL.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	g := &trust.Gate{Store: trust.Store{Path: filepath.Join(t.TempDir(), "trust.json")}}
	s.opts.TrustGate = g
	s.askTrust()
	if got := s.sheetText(t); !strings.Contains(got, ".janetcode/skills") || !strings.Contains(got, "/trust full") {
		t.Fatalf("first-run sheet = %q", got)
	}
	if err := s.runCommand("/trust instructions session"); err != nil {
		t.Fatal(err)
	}
	if st := g.State(root); st.Level != trust.Instructions || st.Source != "session" {
		t.Fatalf("state after /trust instructions session = %+v", st)
	}
	found := false
	for _, e := range s.sess().Events() {
		found = found || e.Type == session.TypeTrust && e.Name == "instructions"
	}
	if !found {
		t.Fatal("the change is not in the session log")
	}
	if err := s.runCommand("/trust"); err != nil {
		t.Fatal(err)
	}
	if got := s.sheetText(t); !strings.Contains(got, "project trust: instructions (this session only)") {
		t.Fatalf("report = %q", got)
	}
}
