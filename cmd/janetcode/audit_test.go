package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
)

// --audit prints the session from its record and says whether the audit
// log still vouches for it; a tampered session fails it.
func TestAuditModeReadsAndVerifies(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	dir, err := session.Dir()
	if err != nil {
		t.Fatal(err)
	}
	s, err := session.New(dir, "/work", "p", "m")
	if err != nil {
		t.Fatal(err)
	}
	m := llm.TextMessage(llm.RoleUser, "clean the build")
	for _, e := range []session.Event{
		{Type: session.TypeUserMessage, Surface: true, Turn: 1, Message: &m},
		{Type: session.TypePermission, Turn: 1, CallID: "c1", Name: "bash", Reason: "rule bash(rm -rf build)"},
		{Type: session.TypeToolCall, Turn: 1, CallID: "c1", Name: "bash", Arguments: `{"command":"rm -rf build"}`},
		{Type: session.TypeToolResult, Turn: 1, CallID: "c1", Name: "bash", Output: "(no output)"},
	} {
		if _, err := s.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	var buf bytes.Buffer
	if err := auditMode(&buf, s.Meta.ID); err != nil {
		t.Fatalf("intact session: %v\n%s", err, buf.String())
	}
	for _, want := range []string{"user       clean the build", "permission bash allowed by rule bash(rm -rf build)", "call       bash", "audit: intact — 4 events"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("--audit output lacks %q:\n%s", want, buf.String())
		}
	}

	b, _ := os.ReadFile(s.Path())
	os.WriteFile(s.Path(), bytes.Replace(b, []byte("rm -rf build"), []byte("ls"), 1), 0o600)
	buf.Reset()
	if err := auditMode(&buf, s.Meta.ID); !errors.Is(err, errAuditBroken) || !strings.Contains(buf.String(), "audit: BROKEN") {
		t.Fatalf("tampered session: %v\n%s", err, buf.String())
	}
}
