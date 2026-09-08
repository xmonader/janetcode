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

func TestSyncOrRotateOnShrink(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(dir, "/w", "p", "m")
	msgs := []llm.Message{llm.TextMessage(llm.RoleUser, "a"), llm.TextMessage(llm.RoleAssistant, "b"), llm.TextMessage(llm.RoleUser, "c")}
	same, err := s.SyncOrRotate(msgs)
	if err != nil || same != s {
		t.Fatalf("grow: %v %v", err, same == s)
	}
	compacted := []llm.Message{llm.TextMessage(llm.RoleUser, "summary")}
	ns, err := s.SyncOrRotate(compacted)
	if err != nil || ns == s || ns.Meta.Cwd != "/w" {
		t.Fatalf("rotate: %v same=%v meta=%+v", err, ns == s, ns.Meta)
	}
	if _, old, _ := Open(dir, s.Meta.ID); len(old) != 3 {
		t.Fatalf("old session damaged: %d messages", len(old))
	}
	if _, cur, _ := Open(dir, ns.Meta.ID); len(cur) != 1 || cur[0].Text() != "summary" {
		t.Fatalf("new session = %+v", cur)
	}
}

func TestSavePinsSurvivesOpenAndRotate(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir, "/work", "p", "m")
	if err != nil {
		t.Fatal(err)
	}
	msgs := []llm.Message{llm.TextMessage(llm.RoleUser, "hi"), llm.TextMessage(llm.RoleAssistant, "yo")}
	if err := s.Sync(msgs); err != nil {
		t.Fatal(err)
	}
	if err := s.SavePins([]string{"use tabs", "never touch vendor/"}, msgs); err != nil {
		t.Fatal(err)
	}
	re, back, err := Open(dir, s.Meta.ID)
	if err != nil || len(back) != 2 || len(re.Meta.Pins) != 2 || re.Meta.Pins[1] != "never touch vendor/" {
		t.Fatalf("reopened: pins=%v msgs=%d err=%v", re.Meta.Pins, len(back), err)
	}
	ns, err := re.SyncOrRotate([]llm.Message{llm.TextMessage(llm.RoleUser, "compacted")})
	if err != nil || ns == re || len(ns.Meta.Pins) != 2 {
		t.Fatalf("rotate: same=%v pins=%v err=%v", ns == re, ns.Meta.Pins, err)
	}
}

func TestForkTitleAndSearch(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir, "/work", "p", "m")
	if err != nil {
		t.Fatal(err)
	}
	msgs := []llm.Message{
		llm.TextMessage(llm.RoleUser, "Please add rate limiting to the API server."),
		llm.TextMessage(llm.RoleAssistant, "Added a token bucket in limiter.go."),
	}
	if err := s.Sync(msgs); err != nil {
		t.Fatal(err)
	}
	if got := AutoTitle(msgs); got != "Please add rate limiting to the API server" {
		t.Fatalf("AutoTitle = %q", got)
	}
	if err := s.SetTitle("rate limiting", msgs); err != nil {
		t.Fatal(err)
	}
	if err := s.SavePins([]string{"pin"}, msgs); err != nil {
		t.Fatal(err)
	}
	f, err := Fork(dir, s, msgs, "")
	if err != nil {
		t.Fatal(err)
	}
	if f.Meta.ID == s.Meta.ID || f.Meta.Title != "fork of rate limiting" || f.Meta.ForkOf != s.Meta.ID || len(f.Meta.Pins) != 1 {
		t.Fatalf("fork meta = %+v", f.Meta)
	}
	// Appending to the fork must not rewrite the original.
	more := append(append([]llm.Message(nil), msgs...), llm.TextMessage(llm.RoleUser, "now use redis"))
	if err := f.Sync(more); err != nil {
		t.Fatal(err)
	}
	_, orig, _ := Open(dir, s.Meta.ID)
	re, back, _ := Open(dir, f.Meta.ID)
	if len(orig) != 2 || len(back) != 3 || re.Meta.Title != "fork of rate limiting" {
		t.Fatalf("orig=%d fork=%d title=%q", len(orig), len(back), re.Meta.Title)
	}
	infos, _ := List(dir, "/work")
	if len(infos) != 2 || infos[0].Label() != "fork of rate limiting" {
		t.Fatalf("infos = %+v", infos)
	}
	hits, err := Search(dir, "/work", "token BUCKET", 10)
	if err != nil || len(hits) != 2 || hits[0].Info.Meta.ID != f.Meta.ID || hits[0].Role != llm.RoleAssistant || !strings.Contains(hits[0].Snippet, "token bucket") {
		t.Fatalf("hits = %+v err=%v", hits, err)
	}
	if hits, _ := Search(dir, "/work", "redis", 10); len(hits) != 1 || hits[0].Info.Meta.ID != f.Meta.ID {
		t.Fatalf("redis hits = %+v", hits)
	}
	if hits, _ := Search(dir, "/elsewhere", "redis", 10); len(hits) != 0 {
		t.Fatal("cwd filter ignored")
	}
	if hits, _ := Search(dir, "", "nothing-here", 10); len(hits) != 0 {
		t.Fatal("phantom hit")
	}
}
