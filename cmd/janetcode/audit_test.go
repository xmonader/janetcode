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
	if err := auditMode(&buf, s.Meta.ID, ""); err != nil {
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
	if err := auditMode(&buf, s.Meta.ID, ""); !errors.Is(err, errAuditBroken) || !strings.Contains(buf.String(), "audit: BROKEN") {
		t.Fatalf("tampered session: %v\n%s", err, buf.String())
	}
	buf.Reset()
	if err := auditMode(&buf, s.Meta.ID, "bash"); !errors.Is(err, errAuditBroken) || strings.Contains(buf.String(), "Deny rule preview") {
		t.Fatalf("preview on tampered session: %v\n%s", err, buf.String())
	}
}

func TestAuditDenyPreview(t *testing.T) {
	s, err := session.New(t.TempDir(), t.TempDir(), "p", "m")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range []session.Event{
		{Type: session.TypeToolCall, Turn: 1, Name: "bash", Arguments: `{"command":"go test ./..."}`},
		{Type: session.TypeToolCall, Turn: 2, Name: "bash", Arguments: `{"command":"go test ./... && git push origin main"}`},
		{Type: session.TypeToolCall, Turn: 3, Name: "write_file", Arguments: `{"path":"deploy/config.yml","content":"fixture payload"}`},
		{Type: session.TypePermission, Turn: 4, Name: "bash", Denied: true},
	} {
		if _, err := s.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	stdout, stderr := captureStd(t)
	err = run([]string{"--audit", s.Path(), "--deny", "bash(git push *),write_file(write:deploy/**)"})
	out, errOut := stdout(), stderr()
	if err != nil {
		t.Fatalf("preview: %v\n%s\n%s", err, out, errOut)
	}
	for _, want := range []string{"turn 2 ·", "· bash · bash(git push *)", "turn 3 ·", "· write_file · write_file(write:deploy/**)", "2 of 3 audited tool calls matched"} {
		if !strings.Contains(out, want) {
			t.Errorf("preview lacks %q:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{"turn 1 ·", "turn 4 ·", "fixture payload"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("preview unexpectedly contains %q:\n%s", unwanted, out)
		}
	}
}
