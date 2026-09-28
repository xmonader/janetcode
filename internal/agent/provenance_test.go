package agent

import (
	"context"
	"errors"
	"github.com/xmonader/janetcode/internal/change"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/tool"
)

func TestDiffLinesRoundTrips(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	words := []string{"a\n", "b\n", "c\n", "d\n", "e\n", "f"}
	gen := func() []string {
		n := rng.Intn(12)
		out := make([]string, n)
		for i := range out {
			out[i] = words[rng.Intn(len(words))]
		}
		return out
	}
	for i := 0; i < 500; i++ {
		a, b := gen(), gen()
		var got []string
		ai := 0
		for _, e := range change.DiffLines(a, b) {
			switch e.Op {
			case '=':
				if a[ai] != e.Line {
					t.Fatalf("kept line mismatch at %d", ai)
				}
				got = append(got, e.Line)
				ai++
			case '-':
				ai++
			case '+':
				got = append(got, e.Line)
			}
		}
		if ai != len(a) || strings.Join(got, "") != strings.Join(b, "") {
			t.Fatalf("diff of %q → %q does not rebuild b: %q", a, b, got)
		}
		// Applying every hunk's sides in order rebuilds b as well.
		cur := append([]string{}, a...)
		hunks := change.MakeHunks(change.DiffLines(a, b))
		for i := len(hunks) - 1; i >= 0; i-- {
			old, new := hunks[i].Sides()
			at := hunks[i].OldStart - 1
			if at+len(old) > len(cur) || strings.Join(cur[at:at+len(old)], "") != strings.Join(old, "") {
				t.Fatalf("hunk %s old side does not match a=%q: %q", hunks[i].Header(), a, old)
			}
			cur = append(append(append([]string{}, cur[:at]...), new...), cur[at+len(old):]...)
		}
		if strings.Join(cur, "") != strings.Join(b, "") {
			t.Fatalf("hunks of %q → %q rebuild %q", a, b, cur)
		}
	}
	// Big rewrites degrade to replace-all instead of blowing up.
	big := make([]string, 20000)
	for i := range big {
		big[i] = strings.Repeat("x", i%7) + "\n"
	}
	rev := make([]string, len(big))
	for i := range big {
		rev[i] = big[len(big)-1-i]
	}
	if n := len(change.DiffLines(big, rev)); n == 0 {
		t.Fatal("empty diff")
	}
}

func TestProvenanceBlameAndUndoOneChange(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		// turn 1: create, then edit twice with stated intents
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockText, Text: "Create the config file.\nMore detail."},
			{Type: llm.BlockToolUse, ID: "c1", Name: "write_file", Input: []byte(`{"path":"cfg.txt","content":"one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\n"}`)},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockText, Text: "Rename two to TWO."},
			{Type: llm.BlockToolUse, ID: "c2", Name: "edit_file", Input: []byte(`{"path":"cfg.txt","old_string":"two","new_string":"TWO"}`)},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockText, Text: "Rename eight to EIGHT."},
			{Type: llm.BlockToolUse, ID: "c3", Name: "edit_file", Input: []byte(`{"path":"cfg.txt","old_string":"eight","new_string":"EIGHT"}`)},
		}}, StopReason: llm.StopToolUse},
		toolCall("c4", "edit_file", `{"path":"cfg.txt","old_string":"nine","new_string":"NINE"}`), // fails: no change recorded
		text("done"),
	}}
	a, root := newAgent(t, f, nil)
	a.AutoApprove = true
	a.Tools = tool.NewRegistry(tool.WriteFile{Root: root}, tool.EditFile{Root: root})
	a.UndoPath = filepath.Join(t.TempDir(), "u.json")
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "cfg.txt")
	changes := a.Changes()
	if len(changes) != 3 || changes[0].Intent != "Create the config file." || changes[2].Intent != "Rename eight to EIGHT." || changes[1].Tool != "edit_file" || changes[0].Existed || changes[1].Path != path {
		t.Fatalf("changes = %+v", changes)
	}
	if add, del := changes[1].Stats(); add != 1 || del != 1 {
		t.Fatalf("stats = +%d -%d", add, del)
	}
	if groups := a.ChangeGroups(); len(groups) != 3 || groups[0].Turn != 1 || len(groups[0].Changes) != 1 {
		t.Fatalf("groups = %+v", groups)
	}
	if hunks := changes[2].Hunks(); len(hunks) != 1 || !strings.Contains(hunks[0].String(), "-eight\n+EIGHT\n") || hunks[0].Header() != "@@ -5,4 +5,4 @@" {
		t.Fatalf("hunks = %+v", hunks)
	}

	lines, note, err := a.Blame(path)
	if err != nil || note != "" {
		t.Fatalf("blame: %v %q", err, note)
	}
	want := []int{1, 2, 1, 1, 1, 1, 1, 3}
	for i, l := range lines {
		if l.Seq != want[i] {
			t.Fatalf("blame line %d = %+v, want #%d", i+1, l, want[i])
		}
	}

	// Undo only the middle decision: the later EIGHT edit stays.
	done, err := a.UndoChange(2)
	if err != nil || !strings.HasPrefix(done, "reverted 1 hunk(s) of") {
		t.Fatalf("undo #2: %q %v", done, err)
	}
	if data, _ := os.ReadFile(path); string(data) != "one\ntwo\nthree\nfour\nfive\nsix\nseven\nEIGHT\n" {
		t.Fatalf("after undo #2: %q", data)
	}
	if _, err := a.UndoChange(2); err == nil || !strings.Contains(err.Error(), "already undone") {
		t.Fatalf("second undo #2: %v", err)
	}
	if c, _ := a.Change(4); c.Tool != "undo" || !strings.HasPrefix(c.Intent, "undo #2: Rename two") {
		t.Fatalf("revert not recorded: %+v", c)
	}
	// A hunk edited since refuses.
	os.WriteFile(path, []byte("one\ntwo\nthree\nfour\nfive\nsix\nseven\nEIGHT!\n"), 0o644)
	if _, err := a.UndoChange(3); err == nil || !strings.Contains(err.Error(), "no longer applies") {
		t.Fatalf("undo #3 on an edited hunk: %v", err)
	}
	if _, note, _ := a.Blame(path); note == "" {
		t.Fatal("blame should note the outside edit")
	}
	// Undoing the creation needs the file untouched since.
	if _, err := a.UndoChange(1); err == nil || !strings.Contains(err.Error(), "refusing to remove") {
		t.Fatalf("undo #1 on a changed file: %v", err)
	}
	if _, err := a.UndoChange(99); !errors.Is(err, ErrNoSuchChange) {
		t.Fatalf("unknown: %v", err)
	}

	// The record survives a resume.
	if err := a.SaveUndo(); err != nil {
		t.Fatal(err)
	}
	b := &Agent{}
	b.UndoPath = a.UndoPath
	if err := b.LoadUndo(); err != nil {
		t.Fatal(err)
	}
	if got := b.Changes(); len(got) != 4 || !got[1].Undone || got[3].Seq != 4 {
		t.Fatalf("loaded changes = %+v", got)
	}
	if _, _, err := b.Blame(path); err != nil {
		t.Fatal(err)
	}
}

// A resumed session numbers its turns on from the log (RestoreFromLog),
// while the per-process stats counter starts again at zero. A change was
// stamped with the stats counter, so in a resumed session it named a turn
// of the old run: the triage state saw no changed files for the running
// turn, and a proof note folded the verdict of the wrong turn.
func TestChangeTurnIsTheLoggedTurnAfterResume(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("c1", "write_file", `{"path":"a.txt","content":"x\n"}`),
		text("done"),
	}}
	a, root := newAgent(t, f, nil)
	a.AutoApprove = true
	a.Tools = tool.NewRegistry(tool.WriteFile{Root: root})
	a.turnN = 4 // what RestoreFromLog leaves after a four-turn session
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	changes := a.Changes()
	if len(changes) != 1 || changes[0].Turn != a.turnN || a.turnN != 5 {
		t.Fatalf("turnN = %d, changes = %+v", a.turnN, changes)
	}
	if files := a.turnChangedFiles(); len(files) != 1 {
		t.Fatalf("turn's changed files = %q", files)
	}
}
