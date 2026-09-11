package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/hints"
)

// withHints gives the harness a live engine, which newHarness does not,
// because most of the tests in this package want a quiet transcript.
func withHints(h *harness) *hints.Engine {
	e := hints.New(nil)
	h.m.hints = e
	return e
}

func TestARepeatedTestCommandOffersTheGate(t *testing.T) {
	h := newHarness(t)
	withHints(h)
	h.m.observeBang("go test ./...")
	if strings.Contains(plain(h.m.View()), "/verify set") {
		t.Fatal("hinted on the first run of the command")
	}
	h.m.observeBang("go test ./...")
	v := plain(h.m.View())
	if !strings.Contains(v, `/verify set "go test ./..."`) {
		t.Fatalf("second run did not offer the gate:\n%s", v)
	}
}

func TestAnUnrelatedShellCommandIsNotATestCommand(t *testing.T) {
	h := newHarness(t)
	withHints(h)
	h.m.observeBang("git status")
	h.m.observeBang("git status")
	if v := plain(h.m.View()); strings.Contains(v, "/verify set") {
		t.Fatalf("git status was taken for a test command:\n%s", v)
	}
}

func TestThreeRefusalsOfTheSameShapeOfferTheRuleKey(t *testing.T) {
	h := newHarness(t)
	withHints(h)
	h.m.hintState.VerifyConfigured = true
	for i := 0; i < 2; i++ {
		h.m.observeDenial("bash(git push *)")
	}
	if strings.Contains(plain(h.m.View()), "saves a rule") {
		t.Fatal("hinted before the third refusal")
	}
	h.m.observeDenial("bash(git push *)")
	v := plain(h.m.View())
	if !strings.Contains(v, "bash(git push *)") || !strings.Contains(v, "pressing r") {
		t.Fatalf("third refusal did not offer the r key:\n%s", v)
	}
	// Different shapes are counted apart.
	h2 := newHarness(t)
	withHints(h2)
	h2.m.hintState.VerifyConfigured = true
	h2.m.observeDenial("bash(rm *)")
	h2.m.observeDenial("bash(curl *)")
	h2.m.observeDenial("edit_file")
	if v := plain(h2.m.View()); strings.Contains(v, "pressing r") {
		t.Fatalf("three different shapes were counted as one:\n%s", v)
	}
}

func TestABigPasteOffersTheFileReference(t *testing.T) {
	h := newHarness(t)
	withHints(h)
	h.m.hintState.VerifyConfigured = true
	h.m.observePaste(strings.Repeat("a line of log\n", 60))
	if v := plain(h.m.View()); !strings.Contains(v, "@path") {
		t.Fatalf("a sixty-line paste did not mention @path:\n%s", v)
	}
}

func TestAShortStackTraceStillOffersTheFileReference(t *testing.T) {
	h := newHarness(t)
	withHints(h)
	h.m.hintState.VerifyConfigured = true
	h.m.observePaste("panic: runtime error: index out of range\n\ngoroutine 1 [running]:\nmain.main()\n")
	v := plain(h.m.View())
	if !strings.Contains(v, "stack trace") {
		t.Fatalf("a stack trace was not recognised:\n%s", v)
	}
}

func TestASmallOrdinaryPasteSaysNothing(t *testing.T) {
	h := newHarness(t)
	withHints(h)
	h.m.hintState.VerifyConfigured = true
	h.m.observePaste("just three\nshort\nlines\n")
	if v := plain(h.m.View()); strings.Contains(v, "@path attaches") {
		t.Fatalf("an ordinary paste produced a hint:\n%s", v)
	}
}

func TestHintsOffSilencesEverythingAndOnBringsItBack(t *testing.T) {
	h := newHarness(t)
	withHints(h)
	h.keys("/hints off")
	h.enter()
	h.m.observeBang("go test ./...")
	h.m.observeBang("go test ./...")
	if v := plain(h.m.View()); strings.Contains(v, "/verify set") {
		t.Fatalf("a hint arrived with hints off:\n%s", v)
	}
	h.keys("/hints on")
	h.enter()
	// The two runs above were not counted at all: with hints off the
	// observer does no work, which is the point of the switch.
	h.m.observeBang("go test ./...")
	h.m.observeBang("go test ./...")
	if v := plain(h.m.View()); !strings.Contains(v, "/verify set") {
		t.Fatalf("hints did not come back on:\n%s", v)
	}
}

func TestHintsAloneReportsTheState(t *testing.T) {
	h := newHarness(t)
	withHints(h)
	h.keys("/hints")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "hints are on") {
		t.Fatalf("/hints did not report its state:\n%s", v)
	}
}

func TestNoHintInterruptsAPermissionPrompt(t *testing.T) {
	h := newHarness(t)
	withHints(h)
	h.m.pending = &permissionMsg{}
	h.m.observeBang("go test ./...")
	h.m.observeBang("go test ./...")
	if v := plain(h.m.View()); strings.Contains(v, "/verify set") {
		t.Fatalf("a hint landed on top of a permission prompt:\n%s", v)
	}
	h.m.pending = nil
	h.m.noteHint(nil)
	if v := plain(h.m.View()); !strings.Contains(v, "/verify set") {
		t.Fatalf("the deferred hint was lost:\n%s", v)
	}
}

func TestASessionWithNoEngineIsUnchanged(t *testing.T) {
	h := newHarness(t)
	// newHarness supplies no engine at all: every observer has to be a
	// no-op rather than a panic.
	h.m.observeBang("go test ./...")
	h.m.observeBang("go test ./...")
	h.m.observeDenial("bash(rm *)")
	h.m.observePaste(strings.Repeat("x\n", 100))
	h.m.noteHint(nil)
	h.keys("/hints")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "not available") {
		t.Fatalf("/hints without an engine said:\n%s", v)
	}
}

func TestTheHintRecordIsWrittenWhenOneFires(t *testing.T) {
	h := newHarness(t)
	e := withHints(h)
	dir := t.TempDir()
	store := hints.Open(dir, "proj")
	e.Record = func(string) { _ = store.Save(e.Shown()) }
	h.m.observeBang("go test ./...")
	h.m.observeBang("go test ./...")
	if got := store.Shown(); len(got) != 1 || got[0] != "verify-gate" {
		t.Fatalf("recorded %v", got)
	}
	// A new session for the same project does not repeat it.
	h2 := newHarness(t)
	h2.m.hints = hints.New(store.Shown())
	h2.m.observeBang("go test ./...")
	h2.m.observeBang("go test ./...")
	if v := plain(h2.m.View()); strings.Contains(v, "/verify set") {
		t.Fatalf("the hint repeated in a later session:\n%s", v)
	}
}

// TestHintsDoNotRunOnKeystrokes is the leanness gate. The engine may be
// consulted on events; typing must not touch it at all.
func TestHintsDoNotRunOnKeystrokes(t *testing.T) {
	h := newHarness(t)
	e := hints.New(nil)
	var evals int
	e.Rules = []hints.Rule{{
		ID: "counter", When: func(*hints.State) bool { evals++; return false },
		Text: func(*hints.State) string { return "" },
	}}
	h.m.hints = e
	h.keys("hello there, this is a prompt being typed out")
	h.update(tea.KeyMsg{Type: tea.KeyUp})
	h.update(tea.KeyMsg{Type: tea.KeyDown})
	h.m.View()
	h.m.View()
	if evals != 0 {
		t.Fatalf("the hint rules were evaluated %d times while typing and rendering", evals)
	}
}
