package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

func TestRoundTripAppendAndMerge(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir, "/work", "anthropic", "m")
	if err != nil {
		t.Fatal(err)
	}
	msgs := []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}
	if err := s.Sync(msgs); err != nil {
		t.Fatal(err)
	}
	// Simulate a failed call followed by a merged user turn.
	msgs[0].Blocks = append(msgs[0].Blocks, llm.Block{Type: llm.BlockText, Text: " again"})
	msgs = append(msgs, llm.TextMessage(llm.RoleAssistant, "hello"))
	if err := s.Sync(msgs); err != nil {
		t.Fatal(err)
	}
	msgs = append(msgs, llm.ToolResultMessage(llm.ToolResult("t1", "out", true)))
	if err := s.Sync(msgs); err != nil {
		t.Fatal(err)
	}
	if err := s.Sync(msgs); err != nil { // no-op
		t.Fatal(err)
	}
	s2, got, err := Open(dir, s.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if s2.Meta.Cwd != "/work" || s2.Meta.Model != "m" {
		t.Fatalf("meta = %+v", s2.Meta)
	}
	if len(got) != 3 || got[0].Text() != "hi again" || got[1].Text() != "hello" || got[2].Blocks[0].ToolUseID != "t1" || !got[2].Blocks[0].IsError {
		t.Fatalf("got = %+v", got)
	}
	// Continue appending on the reopened session.
	got = append(got, llm.TextMessage(llm.RoleAssistant, "more"))
	if err := s2.Sync(got); err != nil {
		t.Fatal(err)
	}
	_, got2, _ := Open(dir, s.Meta.ID)
	if len(got2) != 4 {
		t.Fatalf("after reopen+append: %d messages", len(got2))
	}
}

func TestTornLastLineIsDropped(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(dir, "/w", "p", "m")
	s.Sync([]llm.Message{llm.TextMessage(llm.RoleUser, "hi")})
	f, _ := os.OpenFile(s.Path(), os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString(`{"type":"message","message":{"role":"assis`)
	f.Close()
	_, got, err := Open(dir, s.Meta.ID)
	if err != nil || len(got) != 1 {
		t.Fatalf("err=%v got=%d", err, len(got))
	}
}

func TestList(t *testing.T) {
	dir := t.TempDir()
	a, _ := New(dir, "/a", "p", "m")
	a.Sync([]llm.Message{llm.TextMessage(llm.RoleUser, "fix the build\nplease"), llm.TextMessage(llm.RoleAssistant, "ok")})
	New(dir, "/b", "p", "m")
	infos, err := List(dir, "/a")
	if err != nil || len(infos) != 1 || infos[0].Messages != 2 || infos[0].Preview != "fix the build" {
		t.Fatalf("infos=%+v err=%v", infos, err)
	}
	if all, _ := List(dir, ""); len(all) != 2 {
		t.Fatalf("all = %d", len(all))
	}
	if none, err := List(filepath.Join(dir, "missing"), ""); err != nil || none != nil {
		t.Fatalf("missing dir: %v %v", none, err)
	}
}

func TestLatestAndInvalidID(t *testing.T) {
	dir := t.TempDir()
	if id, err := Latest(dir, "/nowhere"); err != nil || id != "" {
		t.Fatalf("empty dir: %q %v", id, err)
	}
	a, _ := New(dir, "/a", "p", "m")
	b, _ := New(dir, "/b", "p", "m")
	// Force ordering: IDs start with a timestamp, so rename b to sort later.
	_ = a
	id, err := Latest(dir, "/b")
	if err != nil || id != b.Meta.ID {
		t.Fatalf("latest = %q err=%v", id, err)
	}
	if _, _, err := Open(dir, "../etc/passwd"); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("err = %v", err)
	}
}
