package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestStayArg(t *testing.T) {
	stay, title := StayArg("stay other path")
	if !stay || title != "other path" {
		t.Fatalf("stay title = %v %q", stay, title)
	}
	stay, title = StayArg("redis variant")
	if stay || title != "redis variant" {
		t.Fatalf("plain = %v %q", stay, title)
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
	glued := []llm.Message{llm.TextMessage(llm.RoleUser, "reply with the single word ok[repository map: 839 source files, ranked for this request]")}
	if got := AutoTitle(glued); got != "reply with the single word ok" {
		t.Fatalf("AutoTitle glued map = %q", got)
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

// TestSetStartRoundTripsAndForks checks the piece /eval keep depends on:
// the recorded start state survives a reopen, and a fork inherits it
// rather than starting blank, since a fork's own first turn happens well
// after the git state SetStart described.
func TestSetStartRoundTripsAndForks(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir, "/work", "p", "m")
	if err != nil {
		t.Fatal(err)
	}
	put(t, s, llm.TextMessage(llm.RoleUser, "hi"))
	want := StartState{Head: "abc123", Snap: "def456", At: time.Now().UTC().Truncate(time.Second)}
	if err := s.SetStart(want); err != nil {
		t.Fatal(err)
	}
	s2, _, err := Open(dir, s.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if s2.Meta.Start == nil || *s2.Meta.Start != want {
		t.Fatalf("Start after reopen = %+v, want %+v", s2.Meta.Start, want)
	}
	f, err := Fork(dir, s, "")
	if err != nil {
		t.Fatal(err)
	}
	if f.Meta.Start == nil || *f.Meta.Start != want {
		t.Fatalf("fork Start = %+v, want %+v", f.Meta.Start, want)
	}
	if f.Meta.Start == s.Meta.Start {
		t.Fatal("fork shares the original's Start pointer")
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

// TestSettledSessionsListLast: /resume lists active sessions by last
// activity, settled ones after them; activity in a settled session makes
// it active again.
func TestSettledSessionsListLast(t *testing.T) {
	dir := t.TempDir()
	a, _ := New(dir, "/w", "p", "m")
	put(t, a, llm.TextMessage(llm.RoleUser, "old but active"))
	b, _ := New(dir, "/w", "p", "m")
	put(t, b, llm.TextMessage(llm.RoleUser, "newer, settled"))
	if err := b.SetSettled(true); err != nil {
		t.Fatal(err)
	}
	infos, _ := List(dir, "/w")
	if len(infos) != 2 || infos[0].Meta.ID != a.Meta.ID || !infos[1].Meta.Settled {
		t.Fatalf("order = %+v", infos)
	}
	put(t, b, llm.TextMessage(llm.RoleUser, "back again"))
	if b.Meta.Settled {
		t.Fatal("activity should un-settle")
	}
	if _, msgs, err := Open(dir, b.Meta.ID); err != nil || len(msgs) != 2 {
		t.Fatalf("reopen: %v %d", err, len(msgs))
	}
	infos, _ = List(dir, "/w")
	if infos[0].Meta.ID != b.Meta.ID || infos[0].Meta.Settled {
		t.Fatalf("revived session should lead: %+v", infos)
	}
}

func TestToggleSettled(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(dir, "/w", "p", "m")
	v, err := s.ToggleSettled()
	if err != nil || !v || !s.Meta.Settled {
		t.Fatalf("v=%v settled=%v err=%v", v, s.Meta.Settled, err)
	}
	v, err = s.ToggleSettled()
	if err != nil || v || s.Meta.Settled {
		t.Fatalf("second toggle: v=%v settled=%v err=%v", v, s.Meta.Settled, err)
	}
}

func TestLoopsAndExtraDirsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir, "/w", "p", "m")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetLoops([]LoopSpec{{ID: "loop-1", Every: "5m0s", Prompt: "ping"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetExtraDirs([]string{"/tmp/extra"}); err != nil {
		t.Fatal(err)
	}
	re, _, err := Open(dir, s.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(re.Meta.Loops) != 1 || re.Meta.Loops[0].Prompt != "ping" {
		t.Fatalf("loops = %+v", re.Meta.Loops)
	}
	if len(re.Meta.ExtraDirs) != 1 || re.Meta.ExtraDirs[0] != "/tmp/extra" {
		t.Fatalf("extra = %+v", re.Meta.ExtraDirs)
	}
}

func TestPIDSidecar(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir, "/w", "p", "m")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.PID(); ok {
		t.Fatal("empty session should have no pid")
	}
	if err := s.WritePID(4242); err != nil {
		t.Fatal(err)
	}
	pid, ok := s.PID()
	if !ok || pid != 4242 {
		t.Fatalf("pid = %d %v", pid, ok)
	}
	if err := s.ClearPID(); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.PID(); ok {
		t.Fatal("pid survived clear")
	}
}

// TestLatestSkipsEmptySessions: --continue lands in the newest session
// that has a conversation, not in a newer one that never got a message.
func TestLatestSkipsEmptySessions(t *testing.T) {
	dir := t.TempDir()
	a, _ := New(dir, "/w", "p", "m")
	put(t, a, llm.TextMessage(llm.RoleUser, "real conversation"))
	time.Sleep(1100 * time.Millisecond) // ids carry seconds
	b, _ := New(dir, "/w", "p", "m")
	if err := b.SetTitle("started and abandoned"); err != nil {
		t.Fatal(err)
	}
	if id, err := Latest(dir, "/w"); err != nil || id != a.Meta.ID {
		t.Fatalf("latest = %q (%v), want the one with messages %q", id, err, a.Meta.ID)
	}
	c, _ := New(dir, "/only-empty", "p", "m")
	if err := c.SetTitle("x"); err != nil {
		t.Fatal(err)
	}
	if id, _ := Latest(dir, "/only-empty"); id != c.Meta.ID {
		t.Fatalf("with nothing better, an empty session is still latest: %q", id)
	}
}

// A session is named after the first thing the user said, and janetcode
// writes user messages on the user's behalf: the output of a "!"
// command, a goal round, an undo note. --sessions and the /resume picker
// were full of rows called "I ran this command myself in /tmp/..." and
// "Work on this goal until it is complete or you are blocked".
func TestSyntheticMessagesDoNotNameASession(t *testing.T) {
	for _, c := range []struct {
		text string
		want bool
	}{
		{"fix the failing test", false},
		{"I ran this command myself in /tmp/x:\n$ ls\n<output>\na\n</output>", true},
		{"Work on this goal until it is complete or you are blocked: ship it", true},
		{"Continue the goal: ship it", true},
		{"I undid change #2 (main.go) on my own; later changes to the file stayed.", true},
		{"I reverted the file changes from your last edits:\nmain.go", true},
		{"I reapplied the file changes I had undone:\nmain.go", true},
		{"[image attached]", true},
		{"<output>x</output>", true},
	} {
		if got := Synthetic(c.text); got != c.want {
			t.Errorf("Synthetic(%.40q) = %v, want %v", c.text, got, c.want)
		}
	}

	msgs := []llm.Message{
		llm.TextMessage(llm.RoleUser, "I ran this command myself in /tmp/x:\n$ go test\n<output>\nok\n</output>"),
		llm.TextMessage(llm.RoleAssistant, "noted"),
		llm.TextMessage(llm.RoleUser, "now make the build green"),
	}
	if got := AutoTitle(msgs); got != "now make the build green" {
		t.Fatalf("AutoTitle = %q", got)
	}

	dir := t.TempDir()
	cwd := t.TempDir()
	s, err := New(dir, cwd, "p", "m")
	if err != nil {
		t.Fatal(err)
	}
	put(t, s, msgs...)
	infos, err := List(dir, cwd)
	if err != nil || len(infos) != 1 {
		t.Fatalf("list: %v %d", err, len(infos))
	}
	if got := infos[0].Label(); got != "now make the build green" {
		t.Fatalf("session label = %q", got)
	}
}

// A session that is nothing but machinery still says something rather
// than falling back to its id.
func TestPreviewFallsBackToMachinery(t *testing.T) {
	dir, cwd := t.TempDir(), t.TempDir()
	s, err := New(dir, cwd, "p", "m")
	if err != nil {
		t.Fatal(err)
	}
	put(t, s, llm.TextMessage(llm.RoleUser, "I ran this command myself in /tmp/x:\n$ ls"))
	infos, err := List(dir, cwd)
	if err != nil || len(infos) != 1 {
		t.Fatalf("list: %v %d", err, len(infos))
	}
	if got := infos[0].Label(); !strings.HasPrefix(got, "I ran this command") {
		t.Fatalf("label = %q", got)
	}
}
