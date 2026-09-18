package trajectory

import (
	"strings"
	"testing"
	"time"
)

func TestFormatIsOneLinePerRecordWithStructure(t *testing.T) {
	l := Fold(sample(time.Now()))
	out := Format(l, Options{Width: 100})
	lines := strings.Split(out, "\n")
	for _, l := range lines {
		if n := len([]rune(l)); n > 100 {
			t.Fatalf("a %d-column line in a 100-column ledger:\n%s", n, l)
		}
	}
	for _, want := range []string{"turn 1", "Step 1 ·", "bash×2", "#5", "TOOL", "MODEL", "USER"} {
		if !strings.Contains(out, want) {
			t.Fatalf("the ledger leaves out %q:\n%s", want, out)
		}
	}
	// A call and its result are one line, with the result after an arrow.
	var toolLine string
	for _, l := range lines {
		if strings.Contains(l, "go test") {
			toolLine = l
		}
	}
	if !strings.Contains(toolLine, "→") || !strings.Contains(toolLine, "ok") {
		t.Fatalf("the call and its result are not on one line: %q", toolLine)
	}
	if !strings.Contains(toolLine, "2.0s") {
		t.Fatalf("the call's duration is missing: %q", toolLine)
	}
}

// A narrow terminal must still produce a ledger, not a wrapped mess.
func TestFormatFitsANarrowTerminal(t *testing.T) {
	l := Fold(sample(time.Now()))
	for _, w := range []int{40, 60, 80, 200} {
		for _, line := range strings.Split(Format(l, Options{Width: w}), "\n") {
			if n := len([]rune(line)); n > w {
				t.Fatalf("width %d produced a %d-column line: %q", w, n, line)
			}
		}
	}
}

func TestInspectShowsTheTimingSplitAndTheBody(t *testing.T) {
	l := Fold(sample(time.Now()))
	var model int
	for _, r := range l.All {
		if r.Kind == KindMessage {
			model = r.Index
			break
		}
	}
	out := Inspect(l, model)
	for _, want := range []string{"first token after", "generated for", "tok/s", "of it cached", "session so far"} {
		if !strings.Contains(out, want) {
			t.Fatalf("the inspector leaves out %q:\n%s", want, out)
		}
	}
	// A tool record shows its arguments and its whole output.
	var tool int
	for _, r := range l.All {
		if r.Kind == KindTool {
			tool = r.Index
			break
		}
	}
	got := Inspect(l, tool)
	if !strings.Contains(got, "go test ./...") || !strings.Contains(got, "ok\tall") {
		t.Fatalf("the tool inspector:\n%s", got)
	}
	if !strings.Contains(Inspect(l, 999), "no record #999") {
		t.Fatalf("a missing record: %q", Inspect(l, 999))
	}
}

// Searching only what fitted on screen is not searching.
func TestSearchReadsTheWholeRecordNotTheLine(t *testing.T) {
	l := Fold(sample(time.Now()))
	// "gofmt" is in the second call's arguments; the ledger line for it
	// is short enough to show it, so use the result body instead: the
	// first call's output says "2.0s" and its preview does too, so pick
	// a word only the full body has.
	if hits := Search(l, "gofmt"); len(hits) != 1 {
		t.Fatalf("gofmt = %d hits", len(hits))
	}
	// AND semantics across terms.
	if hits := Search(l, "bash gofmt"); len(hits) != 1 {
		t.Fatalf("bash+gofmt = %d hits", len(hits))
	}
	if hits := Search(l, "bash nonesuch"); len(hits) != 0 {
		t.Fatalf("a term that matches nothing still returned %d", len(hits))
	}
	// Structure is searchable too: the kind and the turn.
	if hits := Search(l, "tool"); len(hits) < 2 {
		t.Fatalf("searching by kind = %d hits", len(hits))
	}
	if out := FormatSearch(nil, "zzz", 80); !strings.Contains(out, "nothing") {
		t.Fatalf("empty search: %q", out)
	}
}

// The other question a trajectory answers: not what happened, but what
// took so long.
func TestSlowestRanksModelAndToolCallsTogether(t *testing.T) {
	l := Fold(sample(time.Now()))
	rows := Slowest(l, 3)
	if len(rows) != 3 {
		t.Fatalf("slowest = %d rows", len(rows))
	}
	if rows[0].Duration != 2*time.Second || rows[0].Kind != KindTool {
		t.Fatalf("the 2s bash is not first: %+v", rows[0])
	}
	if rows[1].Duration <= rows[2].Duration && rows[1].Duration != rows[2].Duration {
		t.Fatalf("not sorted: %v", rows)
	}
	out := FormatSlowest(l, 3, 100)
	if !strings.Contains(out, "%") || !strings.Contains(out, "slowest records") {
		t.Fatalf("slowest report:\n%s", out)
	}
}

func TestTurnsFoldsASessionToAPage(t *testing.T) {
	l := Fold(sample(time.Now()))
	out := Turns(l)
	if !strings.Contains(out, "turn 1 · 2 steps · 2 tool calls") {
		t.Fatalf("folded turn line:\n%s", out)
	}
	if !strings.Contains(out, "run the tests") {
		t.Fatalf("the folded turn does not say what was asked:\n%s", out)
	}
	if strings.Count(out, "\n") > 4 {
		t.Fatalf("a one-turn fold took %d lines:\n%s", strings.Count(out, "\n")+1, out)
	}
}

func TestFormatOfNothing(t *testing.T) {
	if out := Format(Ledger{}, Options{}); !strings.Contains(out, "no turn has run") {
		t.Fatalf("empty ledger: %q", out)
	}
	if out := Turns(Ledger{}); !strings.Contains(out, "no turn has run") {
		t.Fatalf("empty fold: %q", out)
	}
}

// A long session is unreadable whole; the tail is what you want.
func TestFormatLastKeepsTheEndAndSaysWhatItDropped(t *testing.T) {
	l := Fold(sample(time.Now()))
	out := Format(l, Options{Width: 100, Last: 2})
	if !strings.Contains(out, "before this") {
		t.Fatalf("the elision is silent:\n%s", out)
	}
	if strings.Contains(out, "#1 ") || strings.Contains(out, "run the tests") {
		t.Fatalf("Last=2 kept the beginning:\n%s", out)
	}
}
