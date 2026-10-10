package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/gitx/gitxtest"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/proof"
	"github.com/xmonader/janetcode/internal/snapshot"
	"github.com/xmonader/janetcode/internal/tool"
)

func TestReadBlameShown(t *testing.T) {
	for out, want := range map[string][2]int{
		"3\ta\n4\tb\n5\tc\n... (2 more lines)\n":       {3, 5},
		"12:abc\tx\n13:dqe\ty\n":                       {12, 13},
		"(file has 3 lines; offset 9 is past the end)": {0, 0},
		"# notebook cell\nprint(1)\n":                  {0, 0},
	} {
		lo, hi := readBlameShown(out)
		if lo != want[0] || hi != want[1] {
			t.Errorf("readBlameShown(%q) = %d,%d want %v", out, lo, hi, want)
		}
	}
}

// Only ranges an earlier turn wrote with an intent are noted: not the
// running turn's own, not an undone change, not an undo, not a change
// with no intent, not a single line.
func TestReadBlameSessionSpans(t *testing.T) {
	when := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	by := map[int]Change{
		1: {Seq: 1, Turn: 1, Intent: "fix the off-by-one in paging", When: when},
		2: {Seq: 2, Turn: 3, Intent: "this turn's own edit"},
		3: {Seq: 3, Turn: 1, Intent: "reverted later", Undone: true},
		4: {Seq: 4, Turn: 1, Tool: "undo", Intent: "undo #9"},
		5: {Seq: 5, Turn: 2},
		6: {Seq: 6, Turn: 2, Intent: "one line"},
	}
	owners := []int{0, 1, 1, 1, 2, 2, 3, 3, 4, 4, 5, 5, 6, 1, 1}
	got := readBlameSessionSpans(owners, by, 3, 1, len(owners), map[int]string{1: "go test ./..."})
	if len(got) != 2 {
		t.Fatalf("spans = %+v", got)
	}
	if got[0].from != 2 || got[0].to != 4 || got[0].label != "turn 1 (#1, 2026-10-01): fix the off-by-one in paging · checked by go test ./..." {
		t.Fatalf("first span = %+v", got[0])
	}
	if got[1].from != 14 || got[1].to != 15 {
		t.Fatalf("second span = %+v", got[1])
	}
	// Only the shown window is attributed.
	if got := readBlameSessionSpans(owners, by, 3, 3, 3, nil); len(got) != 0 {
		t.Fatalf("a one-line window of a range was noted: %+v", got)
	}
	if got := readBlameSessionSpans(owners, by, 3, 3, 4, nil); len(got) != 1 || got[0].from != 3 || got[0].to != 4 {
		t.Fatalf("window spans = %+v", got)
	}
	// A window past the end of what was blamed is cut to it.
	if got := readBlameSessionSpans(owners[:3], by, 3, 1, 40, nil); len(got) != 1 || got[0].to != 3 {
		t.Fatalf("clamped spans = %+v", got)
	}
}

func TestReadBlameCommittedSpans(t *testing.T) {
	c1, c2, zero := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("0", 40)
	lines := []proof.Line{
		{Commit: c1, Intent: "guard empty pages", Verdict: "verified"},
		{Commit: c1, Intent: "guard empty pages", Verdict: "verified"},
		{Commit: c2}, {Commit: c2}, // a human commit: no proof note
		{Commit: zero, Intent: "x"}, {Commit: zero, Intent: "x"}, // not committed yet
		{Commit: c1, Intent: "guard empty pages"},
	}
	got := readBlameCommittedSpans(lines, 1, 7)
	if len(got) != 1 || got[0].from != 1 || got[0].to != 2 || got[0].label != "commit "+proof.Short(c1)+" (verified): guard empty pages" {
		t.Fatalf("spans = %+v", got)
	}
}

func TestReadBlameFormatBudget(t *testing.T) {
	if readBlameFormat(nil) != "" {
		t.Fatal("no spans gave a note")
	}
	var spans []readBlameSpan
	for i := range 6 {
		spans = append(spans, readBlameSpan{from: i * 10, to: i*10 + 1 + i, label: strings.Repeat("w", 150)})
	}
	note := readBlameFormat(spans)
	if len(note) > readBlameBudget {
		t.Fatalf("note is %d bytes, budget %d", len(note), readBlameBudget)
	}
	if strings.Contains(note, "L0–1 ") {
		t.Fatalf("the narrowest range was kept over wider ones: %s", note)
	}
	if n := strings.Count(note, "\nL"); n == 0 || n > readBlameMaxSpans {
		t.Fatalf("%d ranges in %q", n, note)
	}
}

// End to end: a file written in turn 1 for a stated reason carries the
// note when turn 2 reads it, not when turn 1 itself reads it back, and
// not when the feature is off.
func TestReadBlameOnARead(t *testing.T) {
	content := `{"path":"page.py","content":"def page(items, n):\n    if n <= 0:\n        return []\n    return items[:n]\n"}`
	read := `{"path":"page.py"}`
	f := &fake{responses: []*llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockText, Text: "Guard a non-positive page size."},
			{Type: llm.BlockToolUse, ID: "w1", Name: "write_file", Input: []byte(content)},
		}}, StopReason: llm.StopToolUse},
		toolCall("r1", "read_file", read),
		text("done"),
		toolCall("r2", "read_file", read),
		text("read"),
	}}
	a, root := newAgent(t, f, nil)
	a.AutoApprove = true
	a.ReadBlame, a.Root = true, root
	a.Tools = tool.NewRegistry(tool.ReadFile{Root: root}, tool.WriteFile{Root: root})
	a.UndoPath = filepath.Join(t.TempDir(), "u.json")
	if err := a.Run(context.Background(), "fix paging", nil); err != nil {
		t.Fatal(err)
	}
	if out := lastToolResult(a); strings.Contains(out, "[blame") {
		t.Fatalf("the turn's own lines were noted: %s", out)
	}
	if err := a.Run(context.Background(), "tidy paging", nil); err != nil {
		t.Fatal(err)
	}
	out := lastToolResult(a)
	if !strings.Contains(out, "[blame: lines an earlier agent turn wrote on purpose]\nL1–4 turn 1 (#1, ") || !strings.Contains(out, "Guard a non-positive page size.") {
		t.Fatalf("read result = %q", out)
	}
	for _, off := range []func(){func() { a.ReadBlame = false }, func() { a.ReadBlame = true; a.Off = map[string]bool{"readblame": true} }} {
		off()
		if note := a.readBlame(context.Background(), "read_file", []byte(read), "1\tdef page(items, n):\n2\t    if n <= 0:\n", false); note != "" {
			t.Fatalf("note while off: %q", note)
		}
	}
	a.Off = nil
	if note := a.readBlame(context.Background(), "read_file", []byte(read), "Error: x", true); note != "" {
		t.Fatalf("a failed read was noted: %q", note)
	}
	if note := a.readBlame(context.Background(), "grep", []byte(read), "1\tx\n2\ty\n", false); note != "" {
		t.Fatalf("grep was noted: %q", note)
	}
}

// Lines a committed session wrote are attributed from its proof note; a
// repository with no proof notes is asked once and then left alone.
func TestReadBlameCommitted(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	gitxtest.InitRepo(t, root)
	os.WriteFile(filepath.Join(root, "page.py"), []byte("def page(items, n):\n    if n <= 0:\n        return []\n    return items[:n]\n"), 0o644)
	readBlameGit(t, root, "add", "-A")
	readBlameGit(t, root, "commit", "-q", "-m", "paging")
	repo, err := snapshot.Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	a := &Agent{ReadBlame: true}
	a.Root, a.Snapshots = root, repo
	shown := "1\tdef page(items, n):\n2\t    if n <= 0:\n3\t        return []\n"
	if note := a.readBlame(ctx, "read_file", []byte(`{"path":"page.py"}`), shown, false); note != "" || !a.readBlameCache.noNotes {
		t.Fatalf("no notes: note %q, cached %v", note, a.readBlameCache.noNotes)
	}
	sha := readBlameGit(t, root, "rev-parse", "HEAD")
	rec := proof.Record{Version: proof.Version, Session: "s1",
		Files: []proof.File{{Path: "page.py", Changes: []proof.ChangeRef{{Seq: 1, Turn: 1, Intent: "guard a non-positive page size"}}}},
		Turns: []proof.Turn{{Turn: 1, Verdict: "verified"}}}
	if err := proof.Attach(ctx, root, sha, rec); err != nil {
		t.Fatal(err)
	}
	a.readBlameCache = readBlameState{}
	note := a.readBlame(ctx, "read_file", []byte(`{"path":"page.py"}`), shown, false)
	if !strings.Contains(note, "L1–3 commit "+proof.Short(sha)+" (verified): guard a non-positive page size") {
		t.Fatalf("note = %q", note)
	}
}

func readBlameGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}
