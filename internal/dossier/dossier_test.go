package dossier

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/change"
	"github.com/xmonader/janetcode/internal/session"
)

// dossierRun builds a recorded run: edits and checks in order. An edit
// is "edit <path> <intent>", a check is "check <command> => <output>", a
// gate "gate <command> => <output>" (red when the output is not empty).
func dossierRun(steps ...string) ([]session.Event, []change.Change) {
	var evs []session.Event
	var chs []change.Change
	seq := 0
	for i, s := range steps {
		id := "c" + string(rune('a'+i))
		kind, rest, _ := strings.Cut(s, " ")
		switch kind {
		case "edit":
			path, intent, _ := strings.Cut(rest, " ")
			seq++
			chs = append(chs, change.Change{Seq: seq, Turn: 1, Call: id, Tool: "edit_file", Intent: intent, Path: "/r/" + path, Existed: true})
			evs = append(evs,
				session.Event{Type: session.TypeToolCall, Turn: 1, CallID: id, Name: "edit_file"},
				session.Event{Type: session.TypeFileChange, Turn: 1, CallID: id, Name: "/r/" + path})
		case "check":
			cmd, out, _ := strings.Cut(rest, " => ")
			args, _ := json.Marshal(map[string]string{"command": cmd})
			evs = append(evs,
				session.Event{Type: session.TypeToolCall, Turn: 1, CallID: id, Name: "bash", Arguments: string(args)},
				session.Event{Type: session.TypeToolResult, Turn: 1, CallID: id, Name: "bash", Output: out})
		case "gate":
			cmd, out, _ := strings.Cut(rest, " => ")
			evs = append(evs, session.Event{Type: session.TypeGate, Turn: 1, Name: cmd, Output: out, IsError: out != ""})
		}
	}
	return evs, chs
}

const (
	goRed2 = "--- FAIL: TestParse (0.00s)\n--- FAIL: TestParse/empty (0.00s)\n--- FAIL: TestLex (0.00s)\nFAIL\n[exit code 1]"
	goRed1 = "--- FAIL: TestLex (0.00s)\nFAIL\n[exit code 1]"
	goRed3 = "--- FAIL: TestParse (0.00s)\n--- FAIL: TestLex (0.00s)\n--- FAIL: TestEval (0.00s)\nFAIL\n[exit code 1]"
)

func TestBuildFoldsHypothesesBestStateAndRepro(t *testing.T) {
	evs, chs := dossierRun(
		"edit p/parse.go handle the empty input",
		"check go test ./p/... => "+goRed2,
		"edit p/lex.go lexer keeps trailing space",
		"check go test ./p/... -run TestL => "+goRed1,
		"edit p/eval.go rewrite eval as a loop",
		"check go test ./p/... => "+goRed3,
		"edit p/eval.go guard nil",
	)
	d := Build("s1", "fix the parser", "check failed", "/r", evs, chs)
	if len(d.Hypotheses) != 4 {
		t.Fatalf("hypotheses %+v, want 4", d.Hypotheses)
	}
	want := []string{"ruled out", "ruled out", "worse", "unchecked"}
	for i, h := range d.Hypotheses {
		if h.Verdict != want[i] {
			t.Errorf("hypothesis %d verdict %q, want %q (%s)", i+1, h.Verdict, want[i], h.Evidence)
		}
	}
	if h := d.Hypotheses[0]; h.Files[0] != "p/parse.go" || h.Intent[0] != "handle the empty input" || !strings.Contains(h.Evidence, "3 failing (TestParse, TestParse/empty, TestLex)") {
		// A subtest is listed as the runner printed it.
		t.Errorf("first hypothesis %+v", h)
	}
	if !strings.Contains(d.Hypotheses[2].Evidence, "was 1 before it") {
		t.Errorf("worse evidence %q", d.Hypotheses[2].Evidence)
	}
	if d.Best == nil || d.Best.AfterChange != 2 || d.Best.Failing != 1 {
		t.Fatalf("best %+v, want after change #2 with 1 failing", d.Best)
	}
	if len(d.Best.Since) != 2 || !strings.HasPrefix(d.Best.Since[0], "#3 p/eval.go") {
		t.Errorf("since %v", d.Best.Since)
	}
	if d.Best.WhatIf != "janetcode --what-if s1 --from-turn 1" {
		t.Errorf("what-if %q", d.Best.WhatIf)
	}
	if d.Repro == nil || !d.Repro.Narrowed || d.Repro.Command != "go test ./p/... -run '^(TestParse|TestLex|TestEval)$'" {
		t.Errorf("repro %+v", d.Repro)
	}
	md := d.Markdown()
	for _, s := range []string{"## Smallest reproduction", "**worse**", "## Best state reached", "--what-if s1"} {
		if !strings.Contains(md, s) {
			t.Errorf("markdown lacks %q:\n%s", s, md)
		}
	}
}

func TestBuildReadsARedSuiteBehindAPipeThatExitedZero(t *testing.T) {
	evs, chs := dossierRun(
		"edit m/re.py rewrite",
		"check python -m pytest -q 2>&1 | tail -5 => FAILED test_re.py::test_sub - KeyError\n1 failed, 5 passed in 0.2s\nEXIT=0\n",
	)
	d := Build("s", "", "check failed", "/r", evs, chs)
	if d.LastRed == nil || d.LastRed.Failing != 1 {
		t.Fatalf("last red %+v, want the piped pytest run", d.LastRed)
	}
	// An output filter after the runner is dropped when it is narrowed:
	// the narrowed run is short enough to read whole.
	if d.Repro.Command != "python -m pytest -q 'test_re.py::test_sub'" {
		t.Errorf("repro %+v", d.Repro)
	}
}

func TestBuildDoesNotNarrowACompoundCommand(t *testing.T) {
	for _, cmd := range []string{
		"make gen && go test ./...",
		"go test ./... | sh fix.sh",
		"python - <<'EOF'\nimport pytest\nEOF",
		"cd sub && python -m pytest",
	} {
		evs, chs := dossierRun("edit a.go x", "check "+cmd+" => "+goRed1)
		d := Build("s", "", "x", "/r", evs, chs)
		if d.Repro == nil || d.Repro.Narrowed || d.Repro.Command != cmd {
			t.Errorf("%q: repro %+v, want it as run", cmd, d.Repro)
		}
	}
	// The workspace cd a model puts in front of everything is not compound.
	evs, chs := dossierRun("edit a.go x", "check cd /r && go test ./... => "+goRed1)
	if d := Build("s", "", "x", "/r", evs, chs); d.Repro.Command != "go test ./... -run '^(TestLex)$'" {
		t.Errorf("repro %+v", d.Repro)
	}
}

func TestBuildNarrowsPytestToNodeIDs(t *testing.T) {
	evs, chs := dossierRun("edit a.py x", "check python3 -m pytest -x tests => FAILED tests/test_a.py::test_one - assert 1 == 2\n[exit code 1]")
	d := Build("s", "", "x", "/r", evs, chs)
	if d.Repro.Command != "python3 -m pytest -q 'tests/test_a.py::test_one'" {
		t.Errorf("repro %q", d.Repro.Command)
	}
}

func TestBuildSkipsNonEvidence(t *testing.T) {
	evs, chs := dossierRun(
		"edit a.go x",
		"check go vet ./... && echo built => built\n",                      // exit 0 has no footer and no summary: not evidence
		"check pytest => bash: pytest: command not found\n[exit code 127]", // never ran
		"gate go test ./... => ",                                           // a green gate
	)
	d := Build("s", "", "x", "/r", evs, chs)
	if len(d.Hypotheses) != 1 || d.Hypotheses[0].Verdict != "held" {
		t.Fatalf("hypotheses %+v, want one held by the gate", d.Hypotheses)
	}
	if d.LastRed != nil || d.Repro != nil {
		t.Errorf("a red check was invented: %+v %+v", d.LastRed, d.Repro)
	}
}

func TestBuildNoBestStateWhenTheEndIsTheBest(t *testing.T) {
	evs, chs := dossierRun("edit a.go x", "check go test ./... => "+goRed3, "edit a.go y", "check go test ./... => "+goRed1)
	d := Build("s", "", "x", "/r", evs, chs)
	if d.Best != nil {
		t.Errorf("best %+v, want none: the last check is the best", d.Best)
	}
}

func TestBuildIgnoresUndoneChangesAndBoundsHypotheses(t *testing.T) {
	var steps []string
	for i := 0; i < maxHypotheses+3; i++ {
		steps = append(steps, "edit a.go try", "check go test ./... => "+goRed1)
	}
	evs, chs := dossierRun(steps...)
	chs[0].Undone = true
	d := Build("s", "", "x", "/r", evs, chs)
	if len(d.Hypotheses) != maxHypotheses || d.Dropped != 2 {
		t.Errorf("%d hypotheses, %d dropped; want %d and 2 (one batch was undone)", len(d.Hypotheses), d.Dropped, maxHypotheses)
	}
	if d.Hypotheses[0].N != 3 {
		t.Errorf("numbering starts at %d, want 3", d.Hypotheses[0].N)
	}
}

func TestEmptyDossier(t *testing.T) {
	d := Build("s", "", "error", "/r", nil, nil)
	if !d.Empty() {
		t.Errorf("dossier of nothing is not empty: %+v", d)
	}
}

func TestFailingTestsFormats(t *testing.T) {
	for out, want := range map[string]string{
		"test tests::adds ... FAILED\ntest tests::subs ... ok": "tests::adds",
		"  ✕ rejects a bad patch (3 ms)\n  ✓ applies":          "rejects a bad patch",
		"not ok 2 - parses ranges\nok 3 - steps":               "parses ranges",
		"all good":                                             "",
	} {
		if got := strings.Join(FailingTests(out), ","); got != want {
			t.Errorf("FailingTests(%q) = %q, want %q", out, got, want)
		}
	}
}
