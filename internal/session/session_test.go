package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

// put appends msgs to the log as surface events, the way a turn does, and
// returns the seq of each.
func put(t *testing.T, s *Session, msgs ...llm.Message) []int {
	t.Helper()
	var seqs []int
	for i := range msgs {
		m := msgs[i]
		typ := TypeUserMessage
		if m.Role == llm.RoleAssistant {
			typ = TypeAssistant
		}
		seq, err := s.Append(Event{Type: typ, Surface: true, Message: &m})
		if err != nil {
			t.Fatal(err)
		}
		seqs = append(seqs, seq)
	}
	return seqs
}

func TestRoundTripAppendAndReplace(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir, "/work", "anthropic", "m")
	if err != nil {
		t.Fatal(err)
	}
	hi := llm.TextMessage(llm.RoleUser, "hi")
	seqs := put(t, s, hi)
	// A failed call, then a merged user turn: the grown message replaces the
	// one already logged instead of being appended a second time.
	grown := llm.TextMessage(llm.RoleUser, "hi again")
	if _, err := s.Append(Event{Type: TypeUserMessage, Surface: true, SurfaceOp: SurfaceOpReplace, Replaces: seqs, Message: &grown}); err != nil {
		t.Fatal(err)
	}
	put(t, s, llm.TextMessage(llm.RoleAssistant, "hello"), llm.ToolResultMessage(llm.ToolResult("t1", "out", true)))

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
	// Continue appending on the reopened session: seqs must not restart.
	put(t, s2, llm.TextMessage(llm.RoleAssistant, "more"))
	_, got2, _ := Open(dir, s.Meta.ID)
	if len(got2) != 4 {
		t.Fatalf("after reopen+append: %d messages", len(got2))
	}
	seen := map[int]bool{}
	for _, e := range s2.Events() {
		if seen[e.Seq] {
			t.Fatalf("duplicate seq %d after reopen", e.Seq)
		}
		seen[e.Seq] = true
	}
}

func TestTornLastLineIsDropped(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(dir, "/w", "p", "m")
	put(t, s, llm.TextMessage(llm.RoleUser, "hi"))
	f, _ := os.OpenFile(s.Path(), os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString(`{"type":"event","t":"user/message","message":{"role":"assis`)
	f.Close()
	_, got, err := Open(dir, s.Meta.ID)
	if err != nil || len(got) != 1 {
		t.Fatalf("err=%v got=%d", err, len(got))
	}
}

func TestList(t *testing.T) {
	dir := t.TempDir()
	a, _ := New(dir, "/a", "p", "m")
	put(t, a, llm.TextMessage(llm.RoleUser, "fix the build\nplease"), llm.TextMessage(llm.RoleAssistant, "ok"))
	b, _ := New(dir, "/b", "p", "m")
	put(t, b, llm.TextMessage(llm.RoleUser, "hello"))
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
	put(t, a, llm.TextMessage(llm.RoleUser, "a"))
	b, _ := New(dir, "/b", "p", "m")
	put(t, b, llm.TextMessage(llm.RoleUser, "b"))
	id, err := Latest(dir, "/b")
	if err != nil || id != b.Meta.ID {
		t.Fatalf("latest = %q err=%v", id, err)
	}
	if _, _, err := Open(dir, "../etc/passwd"); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("err = %v", err)
	}
}

func TestSavePinsSurvivesOpen(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir, "/work", "p", "m")
	if err != nil {
		t.Fatal(err)
	}
	put(t, s, llm.TextMessage(llm.RoleUser, "hi"), llm.TextMessage(llm.RoleAssistant, "yo"))
	if err := s.SavePins([]string{"use tabs", "never touch vendor/"}); err != nil {
		t.Fatal(err)
	}
	re, back, err := Open(dir, s.Meta.ID)
	if err != nil || len(back) != 2 || len(re.Meta.Pins) != 2 || re.Meta.Pins[1] != "never touch vendor/" {
		t.Fatalf("reopened: pins=%v msgs=%d err=%v", re.Meta.Pins, len(back), err)
	}
	// Rewriting the metadata must not drop the events it wrote around.
	if len(re.Events()) != 2 {
		t.Fatalf("events after pin rewrite: %d", len(re.Events()))
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
	put(t, s, msgs...)
	if got := AutoTitle(msgs); got != "Please add rate limiting to the API server" {
		t.Fatalf("AutoTitle = %q", got)
	}
	if err := s.SetTitle("rate limiting"); err != nil {
		t.Fatal(err)
	}
	if err := s.SavePins([]string{"pin"}); err != nil {
		t.Fatal(err)
	}
	f, err := Fork(dir, s, "")
	if err != nil {
		t.Fatal(err)
	}
	if f.Meta.ID == s.Meta.ID || f.Meta.Title != "fork of rate limiting" || f.Meta.ForkOf != s.Meta.ID || len(f.Meta.Pins) != 1 {
		t.Fatalf("fork meta = %+v", f.Meta)
	}
	// Appending to the fork must not touch the original.
	put(t, f, llm.TextMessage(llm.RoleUser, "now use redis"))
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

// TestEmptySessionLeavesNoFile: a session that never records an event —
// a start with no key, a TUI opened and closed — leaves nothing on disk
// and nothing in the list; the first event creates the file with its
// header, and a title set before any event does too.
func TestEmptySessionLeavesNoFile(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir, "/w", "p", "m")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.Path()); !os.IsNotExist(err) {
		t.Fatalf("file exists before any event: %v", err)
	}
	if infos, _ := List(dir, "/w"); len(infos) != 0 {
		t.Fatalf("listed an empty session: %+v", infos)
	}
	put(t, s, llm.TextMessage(llm.RoleUser, "hi"))
	if _, err := os.Stat(s.Path()); err != nil {
		t.Fatalf("file missing after the first event: %v", err)
	}
	if _, msgs, err := Open(dir, s.Meta.ID); err != nil || len(msgs) != 1 {
		t.Fatalf("reopen: %v %d", err, len(msgs))
	}
	if infos, _ := List(dir, "/w"); len(infos) != 1 {
		t.Fatalf("list = %+v", infos)
	}
	t2, _ := New(dir, "/w", "p", "m")
	if err := t2.SetTitle("named first"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(t2.Path()); err != nil {
		t.Fatalf("a title must create the file: %v", err)
	}
}
