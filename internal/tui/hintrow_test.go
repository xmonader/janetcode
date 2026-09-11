package tui

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/feature"
	"github.com/xmonader/janetcode/internal/hints"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
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

func TestThreeRefusalsOfTheSameShapeOfferADenyRule(t *testing.T) {
	h := newHarness(t)
	withHints(h)
	h.m.hintState.VerifyConfigured = true
	for i := 0; i < 2; i++ {
		h.m.observeDenial("bash(git push *)")
	}
	if strings.Contains(plain(h.m.View()), "/permissions deny") {
		t.Fatal("hinted before the third refusal")
	}
	h.m.observeDenial("bash(git push *)")
	v := plain(h.m.View())
	// A refusal must never be answered with the key that allows it for
	// ever. The r key is for the opposite case.
	if !strings.Contains(v, "bash(git push *)") || !strings.Contains(v, "/permissions deny") {
		t.Fatalf("third refusal did not offer a deny rule:\n%s", v)
	}
	if strings.Contains(v, "pressing r") {
		t.Fatalf("three refusals were answered with the allow key:\n%s", v)
	}
	// Different shapes are counted apart.
	h2 := newHarness(t)
	withHints(h2)
	h2.m.hintState.VerifyConfigured = true
	h2.m.observeDenial("bash(rm *)")
	h2.m.observeDenial("bash(curl *)")
	h2.m.observeDenial("edit_file")
	if v := plain(h2.m.View()); strings.Contains(v, "/permissions deny") {
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

// Whichever way round it is, /hints has to name the other way round.
func TestHintsAloneReportsTheStateAndHowToChangeIt(t *testing.T) {
	h := newHarness(t)
	e := withHints(h)
	h.keys("/hints")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "/hints off") {
		t.Fatalf("/hints did not say how to silence them:\n%s", v)
	}
	e.Off = true
	h.keys("/hints")
	h.enter()
	v := plain(h.m.View())
	if !strings.Contains(v, "hints are off") || !strings.Contains(v, "/hints on") {
		t.Fatalf("/hints with hints off did not say how to bring them back:\n%s", v)
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

func TestTheFirstSessionInAGoProjectProposesTheCommand(t *testing.T) {
	h := newHarness(t)
	if err := os.WriteFile(filepath.Join(h.m.opts.Cwd, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	line := h.m.firstRunLine()
	if !strings.Contains(line, `/verify set "go test ./..."`) {
		t.Fatalf("first run in a Go project said %q", line)
	}
	if strings.Contains(line, "\n") {
		t.Fatalf("the first-run note is more than one line:\n%s", line)
	}
}

func TestTheFirstRunLineIsSaidOnceAndNotEveryMorning(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := hints.Open(t.TempDir(), "proj")
	open := func() *model {
		sess, err := session.New(t.TempDir(), root, "fake", "m")
		if err != nil {
			t.Fatal(err)
		}
		e := hints.New(store.Shown())
		e.Record = func(string) { _ = store.Save(e.Shown()) }
		ag := &agent.Agent{Provider: &scripted{}, Tools: tool.NewRegistry(), Model: "m", Log: sess}
		m := newModel(Options{Agent: ag, Session: sess, Provider: "fake", Cwd: root, Version: "test", Hints: e})
		m.term.out = io.Discard
		mm, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
		return mm.(*model)
	}
	if v := plain(open().View()); !strings.Contains(v, "Go project") {
		t.Fatalf("the first session said nothing about the project:\n%s", v)
	}
	if v := plain(open().View()); strings.Contains(v, "Go project") {
		t.Fatalf("the second session said it again:\n%s", v)
	}
}

func TestHintsOffSilencesTheFirstRunLineToo(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "go.mod"), []byte("module x\n"), 0o644)
	sess, err := session.New(t.TempDir(), root, "fake", "m")
	if err != nil {
		t.Fatal(err)
	}
	e := hints.New(nil)
	e.Off = true
	ag := &agent.Agent{Provider: &scripted{}, Tools: tool.NewRegistry(), Model: "m", Log: sess}
	m := newModel(Options{Agent: ag, Session: sess, Provider: "fake", Cwd: root, Version: "test", Hints: e})
	m.term.out = io.Discard
	mm, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = mm.(*model)
	if v := plain(m.View()); strings.Contains(v, "Go project") {
		t.Fatalf("hints were off and the first-run line still appeared:\n%s", v)
	}
}

func TestAProjectAlreadySetUpGetsNoFirstRunLine(t *testing.T) {
	h := newHarness(t)
	os.WriteFile(filepath.Join(h.m.opts.Cwd, "go.mod"), []byte("module x\n"), 0o644)
	if err := os.Mkdir(filepath.Join(h.m.opts.Cwd, ".janetcode"), 0o755); err != nil {
		t.Fatal(err)
	}
	if line := h.m.firstRunLine(); line != "" {
		t.Fatalf("a project with a .janetcode was treated as new: %q", line)
	}
}

func TestAResumedSessionGetsNoFirstRunLine(t *testing.T) {
	h := newHarness(t)
	os.WriteFile(filepath.Join(h.m.opts.Cwd, "go.mod"), []byte("module x\n"), 0o644)
	h.m.opts.Resumed = 4
	if line := h.m.firstRunLine(); line != "" {
		t.Fatalf("a resumed session got a first-run line: %q", line)
	}
}

func TestTheFirstRunLineWritesNothingIntoTheProject(t *testing.T) {
	h := newHarness(t)
	os.WriteFile(filepath.Join(h.m.opts.Cwd, "go.mod"), []byte("module x\n"), 0o644)
	before, _ := os.ReadDir(h.m.opts.Cwd)
	h.m.firstRunLine()
	after, _ := os.ReadDir(h.m.opts.Cwd)
	if len(before) != len(after) {
		t.Fatalf("the first run created something: %d → %d entries", len(before), len(after))
	}
}

func TestUsingACommandIsCountedAndStopsTheHintForIt(t *testing.T) {
	h := newHarness(t)
	h.m.opts.Usage = feature.OpenUsage(t.TempDir())
	withHints(h)
	h.keys("/verify")
	h.enter()
	if !h.m.opts.Usage.Used("/verify") {
		t.Fatal("/verify was not counted")
	}
	// And now the hint that would introduce it stays quiet, because the
	// user has met it.
	h.m.observeBang("go test ./...")
	h.m.observeBang("go test ./...")
	if v := plain(h.m.View()); strings.Contains(v, "/verify set \"go test") {
		t.Fatalf("introduced a command the user had just used:\n%s", v)
	}
}

func TestAliasesAreCountedAsTheCommandTheyName(t *testing.T) {
	h := newHarness(t)
	h.m.opts.Usage = feature.OpenUsage(t.TempDir())
	h.keys("/ctx")
	h.enter()
	if !h.m.opts.Usage.Used("/context") {
		t.Fatal("/ctx was not counted as /context")
	}
}

func TestNothingIsCountedWithoutAStore(t *testing.T) {
	h := newHarness(t) // no Usage in the options
	h.keys("/verify")
	h.enter()
	h.m.noteUse("@path")
	// Nothing to assert but the absence of a panic and of a file: a nil
	// store has to be a session that simply does not count.
	if h.m.opts.Usage.Used("/verify") {
		t.Fatal("a nil store reported a use")
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
