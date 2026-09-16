package tui

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/feature"
	"github.com/xmonader/janetcode/internal/hints"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/perm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/settingsapply"
	"github.com/xmonader/janetcode/internal/tool"
)

// withHints gives the harness a live engine, which newHarness does not,
// because most of the tests in this package want a quiet transcript.
func withHints(h *harness) *hints.Engine {
	e := hints.New(nil, nil)
	h.m.hint.engine = e
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
	if !strings.Contains(v, `/verify set go test ./...`) {
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

// observeDenial reads its count from the agent's ledger rather than
// counting in the TUI itself, so a test drives it by recording into the
// ledger the way the agent's authorize path does, then calling
// observeDenial the way answer() does.
func TestThreeRefusalsOfTheSameShapeOfferADenyRule(t *testing.T) {
	h := newHarness(t)
	withHints(h)
	h.m.opts.Agent.Ledger = perm.OpenLedger(t.TempDir(), "proj")
	for i := 0; i < 2; i++ {
		h.m.opts.Agent.Ledger.Record("bash(git push *)", false)
		h.m.observeDenial("bash(git push *)")
	}
	if strings.Contains(plain(h.m.View()), "/permissions deny") {
		t.Fatal("hinted before the third refusal")
	}
	h.m.opts.Agent.Ledger.Record("bash(git push *)", false)
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
	h2.m.opts.Agent.Ledger = perm.OpenLedger(t.TempDir(), "proj")
	for _, shape := range []string{"bash(rm *)", "bash(curl *)", "edit_file"} {
		h2.m.opts.Agent.Ledger.Record(shape, false)
		h2.m.observeDenial(shape)
	}
	if v := plain(h2.m.View()); strings.Contains(v, "/permissions deny") {
		t.Fatalf("three different shapes were counted as one:\n%s", v)
	}
}

// TestNoLedgerObserveDenialIsANoOp: without a ledger wired (an agent
// built before this feature, or a test harness that never sets one),
// observeDenial has nothing to read and must not panic.
func TestNoLedgerObserveDenialIsANoOp(t *testing.T) {
	h := newHarness(t)
	withHints(h)
	h.m.observeDenial("bash(git push *)")
	if v := plain(h.m.View()); strings.Contains(v, "/permissions deny") {
		t.Fatalf("a nil ledger somehow offered a deny rule:\n%s", v)
	}
}

// TestApprovedThreeTimesOffersTheLearnedRule: once the ledger has seen a
// shape approved perm.LearnAfter times with no refusal, the next approval
// of it (as answer() would drive) offers saving it as a rule.
func TestApprovedThreeTimesOffersTheLearnedRule(t *testing.T) {
	h := newHarness(t)
	withHints(h)
	h.m.opts.Agent.Ledger = perm.OpenLedger(t.TempDir(), "proj")
	for i := 0; i < perm.LearnAfter-1; i++ {
		h.m.opts.Agent.Ledger.Record("bash(go test *)", true)
		h.m.observeApproval("bash(go test *)")
	}
	if strings.Contains(plain(h.m.View()), "/permissions allow") {
		t.Fatal("hinted before the learn threshold")
	}
	h.m.opts.Agent.Ledger.Record("bash(go test *)", true)
	h.m.observeApproval("bash(go test *)")
	v := plain(h.m.View())
	if !strings.Contains(v, "bash(go test *)") || !strings.Contains(v, "/permissions allow") {
		t.Fatalf("the learn threshold did not offer a rule:\n%s", v)
	}
}

// TestARefusalStopsTheLearnedRuleFromFiring: a shape refused even once
// is never offered as a candidate, whatever it was approved before.
func TestARefusalStopsTheLearnedRuleFromFiring(t *testing.T) {
	h := newHarness(t)
	withHints(h)
	h.m.opts.Agent.Ledger = perm.OpenLedger(t.TempDir(), "proj")
	for i := 0; i < perm.LearnAfter; i++ {
		h.m.opts.Agent.Ledger.Record("bash(go test *)", true)
	}
	h.m.opts.Agent.Ledger.Record("bash(go test *)", false)
	h.m.observeApproval("bash(go test *)")
	if v := plain(h.m.View()); strings.Contains(v, "/permissions allow") {
		t.Fatalf("a refused shape was still offered as a learned rule:\n%s", v)
	}
}

func TestABigPasteOffersTheFileReference(t *testing.T) {
	h := newHarness(t)
	withHints(h)
	h.m.hint.state.VerifyConfigured = true
	h.m.observePaste(strings.Repeat("a line of log\n", 60))
	if v := plain(h.m.View()); !strings.Contains(v, "@path") {
		t.Fatalf("a sixty-line paste did not mention @path:\n%s", v)
	}
}

func TestAShortStackTraceStillOffersTheFileReference(t *testing.T) {
	h := newHarness(t)
	withHints(h)
	h.m.hint.state.VerifyConfigured = true
	h.m.observePaste("panic: runtime error: index out of range\n\ngoroutine 1 [running]:\nmain.main()\n")
	v := plain(h.m.View())
	if !strings.Contains(v, "stack trace") {
		t.Fatalf("a stack trace was not recognised:\n%s", v)
	}
}

func TestASmallOrdinaryPasteSaysNothing(t *testing.T) {
	h := newHarness(t)
	withHints(h)
	h.m.hint.state.VerifyConfigured = true
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
	e.Record = func(string) { _ = store.Save(e.Shown(), e.Skipped()) }
	h.m.observeBang("go test ./...")
	h.m.observeBang("go test ./...")
	if got, _ := store.Load(); len(got) != 1 || got[0] != "verify-gate" {
		t.Fatalf("recorded %v", got)
	}
	// A new session for the same project does not repeat it.
	h2 := newHarness(t)
	h2.m.hint.engine = hints.New(store.Load())
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
	if !strings.Contains(line, `/verify set go test ./...`) {
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
		e := hints.New(store.Load())
		e.Record = func(string) { _ = store.Save(e.Shown(), e.Skipped()) }
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

// /hints counted every spent rule as a hint given, including the ones
// suppressed in silence because the first-run line had already proposed
// the same thing. It now reports what was said and what was skipped.
func TestHintsReportsShownAndSkippedApart(t *testing.T) {
	h := newHarness(t)
	e := hints.New(nil, nil)
	h.m.hint.engine = e
	e.MarkShown("first-run")
	e.MarkSpent("verify-gate")
	h.keys("/hints")
	h.enter()
	v := plain(h.m.View())
	if !strings.Contains(v, "1 hint shown for this project") || !strings.Contains(v, "1 skipped") {
		t.Fatalf("/hints did not separate said from spent:\n%s", v)
	}
}

func TestHintsOffSilencesTheFirstRunLineToo(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "go.mod"), []byte("module x\n"), 0o644)
	sess, err := session.New(t.TempDir(), root, "fake", "m")
	if err != nil {
		t.Fatal(err)
	}
	e := hints.New(nil, nil)
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
	if v := plain(h.m.View()); strings.Contains(v, "/verify set go test") {
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
	e := hints.New(nil, nil)
	var evals int
	e.Rules = []hints.Rule{{
		ID: "counter", When: func(*hints.State) bool { evals++; return false },
		Text: func(*hints.State) string { return "" },
	}}
	h.m.hint.engine = e
	h.keys("hello there, this is a prompt being typed out")
	h.update(tea.KeyMsg{Type: tea.KeyUp})
	h.update(tea.KeyMsg{Type: tea.KeyDown})
	h.m.View()
	h.m.View()
	if evals != 0 {
		t.Fatalf("the hint rules were evaluated %d times while typing and rendering", evals)
	}
}

// writeTurn is a scripted turn that writes each file in one response and
// then ends.
func writeTurn(tag string, files ...string) []*llm.Response {
	var blocks []llm.Block
	for i, f := range files {
		in, _ := json.Marshal(map[string]string{"path": f, "content": tag + "\n"})
		blocks = append(blocks, llm.Block{Type: llm.BlockToolUse, ID: fmt.Sprintf("%s-%d", tag, i), Name: "write_file", Input: in})
	}
	return []*llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: blocks}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "done "+tag), StopReason: llm.StopEndTurn},
	}
}

// flat is the view with its wrapping undone, for a hint long enough to
// wrap across lines.
func flat(view string) string { return strings.Join(strings.Fields(plain(view)), " ") }

func runPrompt(h *harness, text string) {
	h.keys(text)
	h.enter()
	h.pump(func() bool { return !h.m.turn.running })
}

// Two turns that change the same files in the same order are a procedure,
// and the recipe hint names it — through the real event flow, from the
// agent's own record of the changes.
func TestTwoTurnsWithTheSameChangesOfferARecipe(t *testing.T) {
	var rs []*llm.Response
	rs = append(rs, writeTurn("one", "VERSION", "CHANGELOG")...)
	rs = append(rs, writeTurn("two", "notes.txt")...)
	rs = append(rs, writeTurn("three", "VERSION", "CHANGELOG")...)
	h := newHarness(t, rs...)
	withHints(h)
	h.m.hint.state.VerifyConfigured = true
	h.m.opts.Agent.AutoApprove = true
	runPrompt(h, "bump the version")
	runPrompt(h, "write notes")
	if v := plain(h.m.View()); strings.Contains(v, "/recipe save") {
		t.Fatalf("offered a recipe before anything repeated:\n%s", v)
	}
	runPrompt(h, "bump it again")
	v := flat(h.m.View())
	if !strings.Contains(v, "/recipe save <name>") || !strings.Contains(v, "VERSION") || !strings.Contains(v, "CHANGELOG") {
		t.Fatalf("a repeated turn did not offer a recipe:\n%s", v)
	}
}

func TestARecipeUserIsNotToldAboutRecipes(t *testing.T) {
	var rs []*llm.Response
	rs = append(rs, writeTurn("one", "VERSION", "CHANGELOG")...)
	rs = append(rs, writeTurn("two", "VERSION", "CHANGELOG")...)
	h := newHarness(t, rs...)
	h.m.opts.Usage = feature.OpenUsage(t.TempDir())
	h.m.opts.Usage.Note("/recipe save")
	e := withHints(h)
	h.m.hint.state.VerifyConfigured = true
	h.m.opts.Agent.AutoApprove = true
	runPrompt(h, "bump the version")
	runPrompt(h, "bump it again")
	if v := plain(h.m.View()); strings.Contains(v, "/recipe save") {
		t.Fatalf("taught /recipe save to a user who uses it:\n%s", v)
	}
	if !e.Given("recipe-repeat") {
		t.Fatal("the rule was not spent")
	}
}

// A whole-turn /undo of a turn that changed several files offers the
// partial undo, through the real /undo command.
func TestUndoingAMixedTurnOffersUndoAbout(t *testing.T) {
	h := newHarness(t, writeTurn("one", "a.txt", "b.txt")...)
	withHints(h)
	h.m.hint.state.VerifyConfigured = true
	h.m.opts.Agent.AutoApprove = true
	runPrompt(h, "make two files")
	h.keys("/undo")
	h.enter()
	v := flat(h.m.View())
	if !strings.Contains(v, "↶") {
		t.Fatalf("undo did not run:\n%s", v)
	}
	if !strings.Contains(v, "/undo about <description>") || !strings.Contains(v, "2 separate changes") {
		t.Fatalf("undoing a two-file turn did not offer /undo about:\n%s", v)
	}
}

func TestUndoingAOneFileTurnSaysNothingAboutUndoAbout(t *testing.T) {
	h := newHarness(t, writeTurn("one", "a.txt")...)
	withHints(h)
	h.m.hint.state.VerifyConfigured = true
	h.m.opts.Agent.AutoApprove = true
	runPrompt(h, "make a file")
	h.keys("/undo")
	h.enter()
	if v := plain(h.m.View()); strings.Contains(v, "/undo about") {
		t.Fatalf("a one-file undo offered /undo about:\n%s", v)
	}
}

// Typing /undo about counts as knowing it, although /undo alone does not.
func TestUndoAboutIsCountedAsItsOwnUse(t *testing.T) {
	h := newHarness(t)
	h.m.opts.Usage = feature.OpenUsage(t.TempDir())
	h.m.observeCommand([]string{"/undo", "about", "the", "logging"})
	if !h.m.opts.Usage.Used("/undo about") {
		t.Fatal("/undo about was not counted")
	}
	h.m.observeCommand([]string{"/undo", "preview"})
	if h.m.opts.Usage.Used("/undo preview") {
		t.Fatal("a subcommand no hint teaches was counted")
	}
}

func TestAPromptAskingForTestsOffersAccept(t *testing.T) {
	for _, p := range []string{"add a retry flag and write tests for it", "fix the parser, TDD please", "do this test-first"} {
		h := newHarness(t)
		withHints(h)
		h.m.hint.state.VerifyConfigured = true
		h.m.observePrompt(p)
		if v := plain(h.m.View()); !strings.Contains(v, "/accept <request>") {
			t.Errorf("%q did not offer /accept:\n%s", p, v)
		}
	}
	h := newHarness(t)
	withHints(h)
	h.m.hint.state.VerifyConfigured = true
	h.m.observePrompt("why does the latest test fail?")
	if v := plain(h.m.View()); strings.Contains(v, "/accept") {
		t.Fatalf("a question about a test offered /accept:\n%s", v)
	}
	h.m.pending = &permissionMsg{}
	h.m.observePrompt("write tests for the cache")
	if v := plain(h.m.View()); strings.Contains(v, "/accept <request>") {
		t.Fatalf("the hint landed on a permission prompt:\n%s", v)
	}
}

func TestAVerifiedTurnOffersEvalKeep(t *testing.T) {
	h := newHarness(t)
	withHints(h)
	h.m.hint.state.VerifyConfigured = true
	h.m.applyEvent(agent.Event{Kind: agent.EvVerdict, Text: "⚠ unverified: nothing ran", IsError: true})
	if v := plain(h.m.View()); strings.Contains(v, "/eval keep") {
		t.Fatalf("an unverified turn offered /eval keep:\n%s", v)
	}
	h.m.applyEvent(agent.Event{Kind: agent.EvVerdict, Text: "✓ verified: go test ./... failed earlier and passes now"})
	if v := plain(h.m.View()); !strings.Contains(v, "/eval keep <name>") {
		t.Fatalf("a verified turn did not offer /eval keep:\n%s", v)
	}
}

func TestAttachingAProofNoteOffersProofPush(t *testing.T) {
	h := newHarness(t)
	withHints(h)
	h.m.hint.state.VerifyConfigured = true
	h.m.applyEvent(agent.Event{Kind: agent.EvNotice, Text: "proof: could not attach evidence to abc123: boom"})
	if v := plain(h.m.View()); strings.Contains(v, "/proof push") {
		t.Fatalf("a failed attach offered /proof push:\n%s", v)
	}
	h.m.applyEvent(agent.Event{Kind: agent.EvNotice, Text: agent.ProofAttachedNotice + "abc123 · /proof shows it"})
	if v := plain(h.m.View()); !strings.Contains(v, "/proof push") {
		t.Fatalf("attaching a note did not offer /proof push:\n%s", v)
	}
}

func TestSwitchingModelAfterATurnOffersWhatIf(t *testing.T) {
	h := newHarness(t, &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "hi"), StopReason: llm.StopEndTurn})
	withHints(h)
	h.m.hint.state.VerifyConfigured = true
	// Before any turn, a switch is a choice of model, not a comparison.
	settingsapply.SwitchModel(nil, h.m.opts.Agent, "m0", nil)
	h.m.observeCommand([]string{"/model", "m0"})
	if v := plain(h.m.View()); strings.Contains(v, "--what-if") {
		t.Fatalf("a switch before any turn offered --what-if:\n%s", v)
	}
	runPrompt(h, "hello")
	h.keys("/model m2")
	h.enter()
	v := plain(h.m.View())
	if !strings.Contains(v, "--what-if "+h.m.opts.Session.Meta.ID) || !strings.Contains(v, "--model m2") {
		t.Fatalf("switching model mid-session did not offer --what-if:\n%s", v)
	}
}

// TestBangSeenIsBounded: only test-shaped lines get into the counter,
// but a session can type an unbounded number of distinct ones -- "go
// test ./a", "go test ./b", one per package -- and nothing used to trim
// the map, so it grew for the life of the window.
func TestBangSeenIsBounded(t *testing.T) {
	var h hintsState
	for i := 0; i < maxBangSeen*3; i++ {
		h.bang(fmt.Sprintf("go test ./pkg%d", i))
	}
	if len(h.bangSeen) > maxBangSeen {
		t.Fatalf("bangSeen holds %d entries, over the %d bound", len(h.bangSeen), maxBangSeen)
	}
	// And the only thing the rule asks of it still works.
	h.bang("go test ./x")
	if n := h.bang("go test ./x"); n != 2 {
		t.Fatalf("the same command seen twice counted %d", n)
	}
}
