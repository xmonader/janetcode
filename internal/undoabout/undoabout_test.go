package undoabout

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/change"
	"github.com/xmonader/janetcode/internal/llm"
)

func TestParseArgs(t *testing.T) {
	ok := []struct {
		rest string
		want Args
	}{
		{"", Args{}},
		{"   ", Args{}},
		{"preview", Args{Preview: true}},
		{"3", Args{N: 3}},
		{"about the logging tweak", Args{About: true, Description: "the logging tweak"}},
		// "about" with nothing after it is still About: the caller words
		// the usage error for its own front-end.
		{"about", Args{About: true}},
		{"apply", Args{Apply: true}},
		{"apply --force", Args{Apply: true, Force: true}},
	}
	for _, c := range ok {
		got, err := ParseArgs(c.rest)
		if err != nil {
			t.Errorf("ParseArgs(%q): %v", c.rest, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseArgs(%q) = %+v, want %+v", c.rest, got, c.want)
		}
	}
	for _, bad := range []string{"preview 3", "apply now", "sideways", "3 4", "preview --force"} {
		if _, err := ParseArgs(bad); err == nil {
			t.Errorf("ParseArgs(%q) was accepted", bad)
		}
	}
}

func ch(seq int, path, intent, before, after string) change.Change {
	return change.Change{Seq: seq, Path: path, Intent: intent, Existed: true, Before: []byte(before), After: []byte(after)}
}

// TestResolveSelectionIsOptIn: undo is opt-in. A number the model did not
// place lands on keep, a number that is not a candidate is dropped, and a
// number named twice is placed once.
func TestResolveSelectionIsOptIn(t *testing.T) {
	cands := []change.Change{
		ch(1, "a.go", "rename", "a\n", "b\n"),
		ch(2, "b.go", "logging", "c\n", "d\n"),
		ch(3, "c.go", "unrelated", "e\n", "f\n"),
	}
	sel := resolveSelection(cands, []int{1, 1, 99}, []int{2}, []mixedSeq{{Seq: 2, Why: "both"}}, "because")
	if len(sel.Undo) != 1 || sel.Undo[0].Seq != 1 {
		t.Fatalf("undo = %+v", sel.Undo)
	}
	if len(sel.Keep) != 2 {
		t.Fatalf("keep should hold #2 and the unplaced #3: %+v", sel.Keep)
	}
	if len(sel.Mixed) != 0 {
		t.Fatalf("#2 was already kept; mixed = %+v", sel.Mixed)
	}
	if sel.Why != "because" {
		t.Fatalf("why = %q", sel.Why)
	}

	// A change the model puts only in mixed stays out of both sides.
	sel = resolveSelection(cands, nil, nil, []mixedSeq{{Seq: 2, Why: "both halves"}}, "")
	if len(sel.Mixed) != 1 || sel.Mixed[0].Why != "both halves" {
		t.Fatalf("mixed = %+v", sel.Mixed)
	}
	if len(sel.Undo) != 0 || len(sel.Keep) != 2 {
		t.Fatalf("undo=%+v keep=%+v", sel.Undo, sel.Keep)
	}

	// A change the model also kept, or also called mixed, is not undone:
	// a contradictory reply is not the clear match undo needs, and mixed
	// is what makes apply refuse without --force.
	sel = resolveSelection(cands, []int{1, 2}, []int{1}, []mixedSeq{{Seq: 2, Why: "both"}}, "")
	if len(sel.Undo) != 0 {
		t.Fatalf("a contradicted change was undone: undo = %+v", sel.Undo)
	}
	if len(sel.Mixed) != 1 || sel.Mixed[0].Change.Seq != 2 {
		t.Fatalf("mixed = %+v, want #2", sel.Mixed)
	}
}

func TestParseSelectionReply(t *testing.T) {
	undo, keep, mixed, why, err := parseSelectionReply("here you go:\n```json\n{\"undo\":[1,2],\"keep\":[3],\"mixed\":[{\"seq\":4,\"why\":\"half\"}],\"why\":\" both \"}\n```\nthat's it")
	if err != nil {
		t.Fatal(err)
	}
	if len(undo) != 2 || undo[0] != 1 || len(keep) != 1 || keep[0] != 3 {
		t.Fatalf("undo=%v keep=%v", undo, keep)
	}
	if len(mixed) != 1 || mixed[0].Seq != 4 || mixed[0].Why != "half" {
		t.Fatalf("mixed=%+v", mixed)
	}
	if why != "both" {
		t.Fatalf("why=%q", why)
	}
	if _, _, _, _, err := parseSelectionReply("no json here"); err == nil {
		t.Fatal("prose was parsed as a selection")
	}
}

// TestSelectByKeywordsFallback covers the three answers the fallback can
// give when no model is available.
func TestSelectByKeywordsFallback(t *testing.T) {
	cands := []change.Change{
		ch(1, "internal/log/log.go", "add debug logging", "package log\n", "package log\n\nfunc Debug() {}\n"),
		ch(2, "internal/store/store.go", "rename Put to Set", "func Put() {}\n", "func Set() {}\n"),
	}

	sel := selectByKeywords("the debug logging", cands)
	if !sel.Fallback {
		t.Fatal("the fallback did not mark itself")
	}
	if len(sel.Undo) != 1 || sel.Undo[0].Seq != 1 {
		t.Fatalf("undo = %+v", sel.Undo)
	}
	if !strings.Contains(sel.Why, "keyword overlap") {
		t.Fatalf("why = %q", sel.Why)
	}

	sel = selectByKeywords("quantum entanglement", cands)
	if len(sel.Undo) != 0 || len(sel.Keep) != 2 {
		t.Fatalf("an unmatched description undid something: %+v", sel.Undo)
	}
	if !strings.Contains(sel.Why, "shares a word") {
		t.Fatalf("why = %q", sel.Why)
	}

	sel = selectByKeywords("!!! ???", cands)
	if len(sel.Undo) != 0 || len(sel.Keep) != 2 {
		t.Fatalf("a wordless description undid something: %+v", sel.Undo)
	}
	if !strings.Contains(sel.Why, "no searchable words") {
		t.Fatalf("why = %q", sel.Why)
	}
}

// fakeProvider replies with whatever text it is given.
type fakeProvider struct {
	reply string
	err   error
	seen  string
}

func (f *fakeProvider) Name() string { return "fake" }
func (f *fakeProvider) Stream(ctx context.Context, req llm.Request, emit func(llm.Event)) (*llm.Response, error) {
	if len(req.Messages) > 0 {
		f.seen = req.Messages[0].Text()
	}
	if f.err != nil {
		return nil, f.err
	}
	return &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, f.reply), StopReason: llm.StopEndTurn}, nil
}

// onDisk writes a change's After to a real file, so partitionUndoable can
// find the edit it recorded still in place.
func onDisk(t *testing.T, root, name, before, after string, seq int, intent string) change.Change {
	t.Helper()
	p := filepath.Join(root, name)
	if err := os.WriteFile(p, []byte(after), 0o644); err != nil {
		t.Fatal(err)
	}
	c := ch(seq, p, intent, before, after)
	return c
}

func TestSelect(t *testing.T) {
	root := t.TempDir()
	a := onDisk(t, root, "log.go", "package p\n", "package p\n\nfunc Debug() {}\n", 1, "add debug logging")
	b := onDisk(t, root, "store.go", "package p\n\nfunc Put() {}\n", "package p\n\nfunc Set() {}\n", 2, "rename Put to Set")
	all := []change.Change{a, b}

	t.Run("no changes", func(t *testing.T) {
		if _, err := Select(context.Background(), nil, "m", root, "anything", nil); !errors.Is(err, ErrNoChangesToUndo) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("empty description", func(t *testing.T) {
		if _, err := Select(context.Background(), nil, "m", root, "   ", all); err == nil {
			t.Fatal("an empty description was accepted")
		}
	})

	t.Run("no provider falls back", func(t *testing.T) {
		sel, err := Select(context.Background(), nil, "m", root, "the debug logging", all)
		if err != nil {
			t.Fatal(err)
		}
		if !sel.Fallback {
			t.Fatal("a selection made without a model is not marked as a fallback")
		}
		if len(sel.Undo) != 1 || sel.Undo[0].Seq != 1 {
			t.Fatalf("undo = %+v", sel.Undo)
		}
	})

	t.Run("model reply is used", func(t *testing.T) {
		prov := &fakeProvider{reply: `{"undo":[2],"keep":[1],"mixed":[],"why":"the rename is the one asked for"}`}
		sel, err := Select(context.Background(), prov, "m", root, "the rename", all)
		if err != nil {
			t.Fatal(err)
		}
		if sel.Fallback {
			t.Fatal("a model selection was marked as a fallback")
		}
		if len(sel.Undo) != 1 || sel.Undo[0].Seq != 2 {
			t.Fatalf("undo = %+v", sel.Undo)
		}
		if sel.Why != "the rename is the one asked for" {
			t.Fatalf("why = %q", sel.Why)
		}
		if !strings.Contains(prov.seen, "the rename") || !strings.Contains(prov.seen, "#1") {
			t.Fatalf("the prompt did not carry the description and the candidates: %q", prov.seen)
		}
	})

	t.Run("a broken reply falls back rather than failing", func(t *testing.T) {
		sel, err := Select(context.Background(), &fakeProvider{reply: "I'd rather not"}, "m", root, "the debug logging", all)
		if err != nil {
			t.Fatal(err)
		}
		if !sel.Fallback {
			t.Fatal("an unusable reply did not fall back to keywords")
		}
	})

	t.Run("an unavailable change is neither undone nor kept", func(t *testing.T) {
		gone := ch(3, filepath.Join(root, "deleted.go"), "add a helper", "package p\n", "package p\n\nfunc H() {}\n")
		sel, err := Select(context.Background(), nil, "m", root, "the helper", append(append([]change.Change(nil), all...), gone))
		if err != nil {
			t.Fatal(err)
		}
		if len(sel.Unavailable) != 1 || sel.Unavailable[0].Change.Seq != 3 {
			t.Fatalf("unavailable = %+v", sel.Unavailable)
		}
		if !strings.Contains(sel.Unavailable[0].Why, "gone since") {
			t.Fatalf("why = %q", sel.Unavailable[0].Why)
		}
		for _, c := range append(append([]change.Change(nil), sel.Undo...), sel.Keep...) {
			if c.Seq == 3 {
				t.Fatal("an unavailable change was offered as a choice")
			}
		}
	})

	t.Run("nothing can be reverted any more", func(t *testing.T) {
		gone := ch(4, filepath.Join(root, "also-deleted.go"), "x", "package p\n", "package p\n\nfunc X() {}\n")
		sel, err := Select(context.Background(), nil, "m", root, "anything", []change.Change{gone})
		if err != nil {
			t.Fatal(err)
		}
		if len(sel.Undo) != 0 || len(sel.Keep) != 0 || len(sel.Unavailable) != 1 {
			t.Fatalf("sel = %+v", sel)
		}
		if !strings.Contains(sel.Why, "edited or reverted since") {
			t.Fatalf("why = %q", sel.Why)
		}
	})
}

func TestTouchedPathsAndFingerprint(t *testing.T) {
	sel := Selection{
		Undo:  []change.Change{ch(1, "a.go", "", "", ""), ch(2, "a.go", "", "", "")},
		Keep:  []change.Change{ch(3, "b.go", "", "", "")},
		Mixed: []Mixed{{Change: ch(4, "c.go", "", "", "")}},
	}
	got := TouchedPaths(sel)
	want := []string{"a.go", "b.go", "c.go"}
	if len(got) != len(want) {
		t.Fatalf("TouchedPaths = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("TouchedPaths = %v, want %v", got, want)
		}
	}
	if f := Fingerprint(sel.Undo); f != "1,2" {
		t.Fatalf("Fingerprint = %q", f)
	}
	if f := Fingerprint(nil); f != "" {
		t.Fatalf("Fingerprint(nil) = %q", f)
	}
}

// TestFormatSelection: the listing has to say what would happen, in seq
// order, and what stops /undo apply from doing it.
func TestFormatSelection(t *testing.T) {
	root := "/proj"
	sel := Selection{
		Undo: []change.Change{ch(2, "/proj/log.go", "add debug logging", "a\n", "a\nb\n")},
		Keep: []change.Change{ch(1, "/proj/store.go", "rename Put", "c\n", "d\n")},
		Why:  "only the logging matches",
	}
	out := FormatSelection(root, sel)
	lines := strings.Split(out, "\n")
	if !strings.HasPrefix(lines[0], "[keep] #1  ") {
		t.Fatalf("the listing is not in seq order:\n%s", out)
	}
	if !strings.HasPrefix(lines[1], "[undo] #2  ") {
		t.Fatalf("second line:\n%s", out)
	}
	if !strings.Contains(out, "add debug logging") || !strings.Contains(out, "log.go") {
		t.Fatalf("the listing hides the reason or the path:\n%s", out)
	}
	if !strings.Contains(out, "only the logging matches") {
		t.Fatalf("the explanation is missing:\n%s", out)
	}
	if !strings.HasSuffix(out, "/undo apply reverts the changes marked undo") {
		t.Fatalf("the closing line is wrong:\n%s", out)
	}

	// Mixed and warnings both block apply, and say so.
	sel.Mixed = []Mixed{{Change: ch(3, "/proj/both.go", "", "x\n", "y\n"), Why: "half of it matches"}}
	sel.Warnings = []string{"store.go uses Debug"}
	out = FormatSelection(root, sel)
	if !strings.Contains(out, "half of it matches") || !strings.Contains(out, "store.go uses Debug") {
		t.Fatalf("mixed or warnings missing:\n%s", out)
	}
	if !strings.HasSuffix(out, "--force overrides it") {
		t.Fatalf("apply is not reported as blocked:\n%s", out)
	}

	// Nothing matched at all.
	out = FormatSelection(root, Selection{Keep: []change.Change{ch(1, "/proj/a.go", "", "a\n", "b\n")}})
	if !strings.Contains(out, "nothing matched that description") {
		t.Fatalf("an empty selection is not reported:\n%s", out)
	}

	// The fallback says so up front, and an empty listing says so too.
	out = FormatSelection(root, Selection{Fallback: true})
	if !strings.HasPrefix(out, "no model available for this") || !strings.Contains(out, "no changes to consider") {
		t.Fatalf("empty fallback listing:\n%s", out)
	}

	// An unavailable change is listed as such rather than silently gone.
	out = FormatSelection(root, Selection{
		Undo:        []change.Change{ch(1, "/proj/a.go", "", "a\n", "b\n")},
		Unavailable: []UnavailableChange{{Change: ch(9, "/proj/z.go", "", "", ""), Why: "#9 edited z.go, which is gone since"}},
	})
	if !strings.Contains(out, "unavailable:") || !strings.Contains(out, "gone since") {
		t.Fatalf("unavailable changes are not reported:\n%s", out)
	}
}

func TestFormatUndoReport(t *testing.T) {
	ok := true
	cases := []struct {
		name string
		rep  UndoReport
		want []string
		not  []string
	}{
		{"nothing", UndoReport{}, []string{"nothing was reverted"}, nil},
		{"reverted and checked", UndoReport{Reverted: []string{"#2 log.go"}, Checks: "checks passed", ChecksOK: &ok},
			[]string{"↶ #2 log.go", "checks passed"}, []string{"nothing was reverted"}},
		{"stopped partway", UndoReport{Reverted: []string{"#2 log.go"}, Refused: "#1 no longer applies"},
			[]string{"stopped: #1 no longer applies"}, nil},
		// Nothing verified the result: the note and the language-server
		// fallback are both shown rather than staying silent.
		{"unverified", UndoReport{Reverted: []string{"#2 log.go"}, CheckNote: "no verify commands configured", Diagnostics: "log.go: no problems"},
			[]string{"no verify commands configured", "log.go: no problems"}, nil},
		// A real check run wins: the diagnostics fallback is not shown.
		{"checks win", UndoReport{Reverted: []string{"#2"}, Checks: "checks passed", CheckNote: "no verify commands", Diagnostics: "log.go: no problems"},
			[]string{"checks passed"}, []string{"no verify commands", "no problems"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := FormatUndoReport(c.rep)
			for _, w := range c.want {
				if !strings.Contains(out, w) {
					t.Errorf("want %q in:\n%s", w, out)
				}
			}
			for _, n := range c.not {
				if strings.Contains(out, n) {
					t.Errorf("did not want %q in:\n%s", n, out)
				}
			}
		})
	}
}

func TestParseArgsAgent(t *testing.T) {
	for _, c := range []struct {
		in   string
		want Args
		bad  bool
	}{
		{"agent", Args{AgentList: true}, false},
		{"agent a3", Args{Agent: "a3"}, false},
		{"agent task-2", Args{Agent: "task-2"}, false},
		{"agent a3 b4", Args{}, true},
	} {
		got, err := ParseArgs(c.in)
		if (err != nil) != c.bad || got != c.want {
			t.Errorf("%q = %+v %v", c.in, got, err)
		}
	}
}
