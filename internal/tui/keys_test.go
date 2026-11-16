package tui

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/feature"
	"github.com/xmonader/janetcode/internal/hints"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/tool"
)

func TestEscCancelsRunningTurn(t *testing.T) {
	blocker := &blocking{started: make(chan struct{})}
	opts := fakeOptions(t)
	opts.Agent.Provider = blocker
	h := newHarnessFor(t, opts)
	h.keys("slow")
	h.enter()
	<-blocker.started
	h.update(tea.KeyMsg{Type: tea.KeyEsc})
	h.pump(func() bool { return !h.m.turn.running })
	if v := plain(h.m.View()); !strings.Contains(v, "cancelled") {
		t.Fatalf("expected cancelled marker:\n%s", v)
	}
	// ctrl+c with typed text clears it; when idle and empty it quits.
	h.keys("draft")
	if _, cmd := h.m.Update(tea.KeyMsg{Type: tea.KeyCtrlC}); cmd != nil || h.m.input.Value() != "" {
		t.Fatalf("ctrl+c should clear the input first: cmd=%v value=%q", cmd, h.m.input.Value())
	}
	// The clear armed the quit, so the next press within the window quits.
	_, cmd := h.m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("ctrl+c when idle should quit")
	}
}

func TestDoubleCtrlCForcesQuit(t *testing.T) {
	blocker := &blocking{started: make(chan struct{})}
	opts := fakeOptions(t)
	opts.Agent.Provider = blocker
	h := newHarnessFor(t, opts)
	h.keys("slow")
	h.enter()
	<-blocker.started
	if _, cmd := h.m.Update(tea.KeyMsg{Type: tea.KeyCtrlC}); cmd != nil || !h.m.turn.cancelling {
		t.Fatal("first ctrl+c should cancel, not quit")
	}
	if _, cmd := h.m.Update(tea.KeyMsg{Type: tea.KeyCtrlC}); cmd == nil {
		t.Fatal("second ctrl+c should quit")
	}
}

func TestMentionCompletionAndAttachment(t *testing.T) {
	h := newHarness(t, &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "ok"), StopReason: llm.StopEndTurn})
	root := h.m.opts.Cwd
	os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o644)
	os.WriteFile(filepath.Join(root, "main_test.go"), []byte("package main\n"), 0o644)
	tab := tea.KeyMsg{Type: tea.KeyTab}
	h.keys("explain @m")
	h.update(tab)
	if got := h.m.input.Value(); got != "explain @main" {
		t.Fatalf("after tab: %q", got)
	}
	if v := plain(h.m.View()); !strings.Contains(v, "@main.go") || !strings.Contains(v, "@main_test.go") {
		t.Fatalf("completions not shown:\n%s", v)
	}
	h.keys("_")
	h.update(tab)
	if got := h.m.input.Value(); got != "explain @main_test.go " {
		t.Fatalf("after second tab: %q", got)
	}
	h.enter()
	h.pump(func() bool { return !h.m.turn.running })
	sent := h.m.opts.Agent.Messages[0].Blocks[0].Text
	if !strings.HasPrefix(sent, "explain @main_test.go") || !strings.Contains(sent, "<attached path=\"main_test.go\">\npackage main") {
		t.Fatalf("prompt sent = %q", sent)
	}
	if v := plain(h.m.View()); !strings.Contains(v, "attached main_test.go (13B)") || strings.Contains(v, "<attached") {
		t.Fatalf("view:\n%s", v)
	}
}

func TestCtrlVPastesClipboardImage(t *testing.T) {
	h := newHarness(t, &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "a screenshot"), StopReason: llm.StopEndTurn})
	h.m.clipboard = func() ([]byte, error) { return nil, errors.New("no image in the clipboard") }
	h.keys("look")
	h.update(tea.KeyMsg{Type: tea.KeyCtrlV})
	if h.m.input.Value() != "look" || !strings.Contains(h.m.status, "no image") {
		t.Fatalf("input %q status %q", h.m.input.Value(), h.m.status)
	}
	png := []byte("\x89PNG\r\n\x1a\nfake")
	h.m.clipboard = func() ([]byte, error) { return png, nil }
	h.update(tea.KeyMsg{Type: tea.KeyCtrlV})
	v := h.m.input.Value()
	if !strings.HasPrefix(v, "look @") || !strings.HasSuffix(v, ".png ") {
		t.Fatalf("input after paste: %q", v)
	}
	path := strings.TrimSpace(strings.TrimPrefix(v, "look @"))
	t.Cleanup(func() { os.Remove(path) })
	h.enter()
	h.pump(func() bool { return !h.m.turn.running })
	blocks := h.m.opts.Agent.Messages[0].Blocks
	if len(blocks) != 2 || blocks[1].Type != llm.BlockImage || blocks[1].MediaType != "image/png" {
		t.Fatalf("blocks = %+v", blocks)
	}
}

// TestExactCommandNameLeadsTheMenu: /pr is a prefix of /pr-comments, which
// the command table lists first. Typing /pr and pressing enter took the
// highlighted /pr-comments into the box instead of running /pr, so /pr
// could never be run without an argument or a trailing space. A command
// typed in full must head the menu.
func TestExactCommandNameLeadsTheMenu(t *testing.T) {
	h := newHarness(t)
	h.keys("/pr")
	c, idx := h.m.menu()
	if len(c) < 2 || c[idx].name != "/pr" {
		names := []string{}
		for _, r := range c {
			names = append(names, r.name)
		}
		t.Fatalf("menu for /pr is led by %v, want /pr first", names)
	}
}

func TestMenuNavigation(t *testing.T) {
	h := newHarness(t)
	up := tea.KeyMsg{Type: tea.KeyUp}
	down := tea.KeyMsg{Type: tea.KeyDown}
	h.keys("/")
	c, idx := h.m.menu()
	if len(c) < 5 || idx != 0 {
		t.Fatalf("menu = %d rows idx %d", len(c), idx)
	}
	h.update(down)
	h.update(down)
	if _, idx := h.m.menu(); idx != 2 {
		t.Fatalf("idx after two downs = %d", idx)
	}
	if v := plain(h.m.View()); !strings.Contains(v, "▸ "+c[2].name) {
		t.Fatalf("highlight missing:\n%s", v)
	}
	h.update(up)
	h.update(up)
	h.update(up) // wraps to the last row
	if _, idx := h.m.menu(); idx != len(c)-1 {
		t.Fatalf("wrap idx = %d", idx)
	}
	// Typing resets the highlight; enter on a no-arg command runs it.
	h.keys("he")
	c, idx = h.m.menu()
	if idx != 0 || c[0].name != "/help" {
		t.Fatalf("after typing: %v %d", c, idx)
	}
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "commands") || h.m.input.Value() != "" {
		t.Fatalf("/help did not run from the menu: input %q\n%s", h.m.input.Value(), v)
	}
	// A command that takes arguments is filled in, not run.
	h.keys("/mod")
	h.enter()
	if h.m.input.Value() != "/model " {
		t.Fatalf("input = %q", h.m.input.Value())
	}
	h.inputReset()
	// Path menus: down selects the second file, enter fills it in.
	os.WriteFile(filepath.Join(h.m.opts.Cwd, "alpha.go"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(h.m.opts.Cwd, "beta.go"), []byte("x"), 0o644)
	h.keys("see @")
	h.update(down)
	h.enter()
	if h.m.input.Value() != "see @beta.go " {
		t.Fatalf("input = %q", h.m.input.Value())
	}
	if _, idx := h.m.menu(); idx != 0 {
		t.Fatalf("menu should be closed or reset, idx = %d", idx)
	}
}

func TestPromptHistoryRecall(t *testing.T) {
	h := newHarness(t,
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "a"), StopReason: llm.StopEndTurn},
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "b"), StopReason: llm.StopEndTurn},
	)
	h.keys("first prompt")
	h.enter()
	h.pump(func() bool { return !h.m.turn.running })
	h.keys("second prompt")
	h.enter()
	h.pump(func() bool { return !h.m.turn.running })
	h.keys("dra")
	up := tea.KeyMsg{Type: tea.KeyUp}
	down := tea.KeyMsg{Type: tea.KeyDown}
	h.update(up)
	if h.m.input.Value() != "second prompt" {
		t.Fatalf("after up: %q", h.m.input.Value())
	}
	h.update(up)
	if h.m.input.Value() != "first prompt" {
		t.Fatalf("after up up: %q", h.m.input.Value())
	}
	h.update(up) // at the oldest entry; stays
	h.update(down)
	h.update(down)
	if h.m.input.Value() != "dra" {
		t.Fatalf("draft not restored: %q", h.m.input.Value())
	}
}

func TestReadlineKeysEditTheInput(t *testing.T) {
	h := newHarness(t)
	h.keys("hello world")
	h.update(tea.KeyMsg{Type: tea.KeyCtrlW})
	if got := h.m.input.Value(); got != "hello " {
		t.Fatalf("after ctrl+w: %q", got)
	}
	h.update(tea.KeyMsg{Type: tea.KeyCtrlU})
	if got := h.m.input.Value(); got != "" {
		t.Fatalf("after ctrl+u: %q", got)
	}
	// With nothing to edit, ctrl+u pages the transcript and must not panic.
	h.update(tea.KeyMsg{Type: tea.KeyCtrlU})
	h.keys("abc")
	h.update(tea.KeyMsg{Type: tea.KeyCtrlA})
	h.update(tea.KeyMsg{Type: tea.KeyCtrlK})
	if got := h.m.input.Value(); got != "" {
		t.Fatalf("after ctrl+a ctrl+k: %q", got)
	}
}

// TestStrayApprovalKeyIsNotAPrompt: a lone "y" typed just after a
// permission prompt closed was meant for the prompt; it must not become
// a turn of the model guessing what "y" means.
func TestStrayApprovalKeyIsNotAPrompt(t *testing.T) {
	h := newHarness(t, &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "y?"), StopReason: llm.StopEndTurn})
	h.m.pendingClosedAt = time.Now()
	h.keys("y")
	h.enter()
	if h.m.turn.running || h.m.input.Value() != "" || !strings.Contains(h.m.status, "stray") {
		t.Fatalf("running=%v input=%q status=%q", h.m.turn.running, h.m.input.Value(), h.m.status)
	}
	// Long after the prompt, "y" is just a prompt like any other.
	h.m.pendingClosedAt = time.Now().Add(-time.Minute)
	h.keys("y")
	h.enter()
	if !h.m.turn.running {
		t.Fatal("a y typed long after the prompt should be sent")
	}
	h.pump(func() bool { return !h.m.turn.running })
}

// TestCtrlCWhenIdleArmsFirst: one ctrl+c when idle says "again to quit";
// the second within the window quits; after the window it arms again.
func TestCtrlCWhenIdleArmsFirst(t *testing.T) {
	h := newHarness(t)
	if _, cmd := h.m.Update(tea.KeyMsg{Type: tea.KeyCtrlC}); cmd != nil || !strings.Contains(h.m.status, "again to quit") {
		t.Fatalf("first press should arm: cmd=%v status=%q", cmd, h.m.status)
	}
	if _, cmd := h.m.Update(tea.KeyMsg{Type: tea.KeyCtrlC}); cmd == nil {
		t.Fatal("second press should quit")
	}
	h2 := newHarness(t)
	h2.m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	h2.m.quitArmedAt = time.Now().Add(-time.Minute)
	if _, cmd := h2.m.Update(tea.KeyMsg{Type: tea.KeyCtrlC}); cmd != nil {
		t.Fatal("a press after the window should arm again, not quit")
	}
}

// TestCompletionMenusMatch covers item 12: the "/" menu and the @path menu
// are the same menu — same height whatever they contain, so the transcript
// above does not reflow on every keystroke, and the same keys.
func TestCompletionMenusMatch(t *testing.T) {
	h := newHarness(t)
	os.WriteFile(filepath.Join(h.m.opts.Cwd, "alpha.go"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(h.m.opts.Cwd, "alpine.go"), []byte("x"), 0o644)
	h.update(tea.WindowSizeMsg{Width: 100, Height: 30})

	closed := h.m.viewport.Height
	h.keys("/")
	openHeight := h.m.viewport.Height
	if openHeight >= closed {
		t.Fatal("the menu should take room from the transcript")
	}
	// Narrowing the menu must not move the viewport again.
	for _, s := range []string{"c", "o", "n"} {
		h.keys(s)
		if h.m.viewport.Height != openHeight {
			t.Fatalf("typing %q resized the transcript: %d → %d", s, openHeight, h.m.viewport.Height)
		}
	}
	rows := func() int { return strings.Count(h.m.footerView(), "\n") }
	withOne := rows()
	h.inputReset()
	h.keys("/")
	if got := rows(); got != withOne {
		t.Errorf("a full menu is %d rows and a one-row menu is %d", got, withOne)
	}
	// The @path menu is the same size, and says the same thing at the foot.
	h.inputReset()
	h.keys("see @")
	if got := rows(); got != withOne {
		t.Errorf("the @path menu is %d rows, the command menu %d", got, withOne)
	}
	if !strings.Contains(plain(h.m.footerView()), "tab or enter accepts") {
		t.Errorf("the @path menu is missing the key line:\n%s", plain(h.m.footerView()))
	}
	// Tab extends to what the matches share, in both menus.
	h.inputReset()
	h.keys("see @a")
	h.update(tea.KeyMsg{Type: tea.KeyTab})
	if got := h.m.input.Value(); got != "see @alp" {
		t.Errorf("path tab = %q, want the shared prefix", got)
	}
	h.inputReset()
	h.keys("/conn")
	h.update(tea.KeyMsg{Type: tea.KeyTab})
	if got := h.m.input.Value(); got != "/connect " {
		t.Errorf("command tab = %q", got)
	}
	// Down then tab takes the highlighted row in both.
	h.inputReset()
	h.keys("see @alp")
	h.update(tea.KeyMsg{Type: tea.KeyDown})
	h.update(tea.KeyMsg{Type: tea.KeyTab})
	if got := h.m.input.Value(); got != "see @alpine.go " {
		t.Errorf("path tab after down = %q", got)
	}
	h.inputReset()
	h.keys("/co")
	c, _ := h.m.menu()
	h.update(tea.KeyMsg{Type: tea.KeyDown})
	h.update(tea.KeyMsg{Type: tea.KeyTab})
	if got := h.m.input.Value(); got != c[1].name+" " {
		t.Errorf("command tab after down = %q, want %q", got, c[1].name+" ")
	}
}

// TestPasteChip: a large paste becomes one chip in the input, deletes as
// one unit, and is sent in full.
func TestPasteChip(t *testing.T) {
	h := newHarness(t, &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "ok"), StopReason: llm.StopEndTurn})
	body := strings.Repeat("a line of a log file\n", 40)
	h.keys("look at ")
	h.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(body), Paste: true})
	if got := h.m.input.Value(); got != "look at [paste #1 +41 lines]" {
		t.Fatalf("input = %q", got)
	}
	if !strings.Contains(plain(h.m.View()), "[paste #1 +41 lines]") {
		t.Fatal("the chip is not shown in the input")
	}
	// A small paste is left alone.
	h.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" and this"), Paste: true})
	if got := h.m.input.Value(); got != "look at [paste #1 +41 lines] and this" {
		t.Fatalf("small paste = %q", got)
	}
	for range " and this" {
		h.update(tea.KeyMsg{Type: tea.KeyBackspace})
	}
	// One more backspace takes the whole chip.
	h.update(tea.KeyMsg{Type: tea.KeyBackspace})
	if got := h.m.input.Value(); got != "look at " {
		t.Fatalf("backspace over the chip left %q", got)
	}
	h.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(body), Paste: true})
	h.enter()
	h.pump(func() bool { return !h.m.turn.running })
	sent := h.m.opts.Agent.Messages[0].Text()
	if !strings.Contains(sent, body) {
		t.Fatalf("the paste was not re-inflated on submit: %q", sent)
	}
	if strings.Contains(sent, "[paste #") {
		t.Fatalf("the chip was sent verbatim: %q", sent)
	}
}

// TestPasteNeverSubmitsItself: a paste ending in a newline must not send
// the prompt.
func TestPasteNeverSubmitsItself(t *testing.T) {
	h := newHarness(t)
	h.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(strings.Repeat("x\n", 30)), Paste: true})
	if h.m.turn.running {
		t.Fatal("a paste submitted the prompt")
	}
	if h.m.input.Value() == "" {
		t.Fatal("the paste did not reach the input")
	}
}

// TestPromptHistoryOutlivesTheProcess: prompts are written to a file per
// working directory, with a flag for "!" commands, and a new model in the
// same directory recalls them.
func TestPromptHistoryOutlivesTheProcess(t *testing.T) {
	h := newHarness(t, &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "ok"), StopReason: llm.StopEndTurn})
	if h.m.hist.file == "" {
		t.Fatal("no history file for the session")
	}
	h.keys("first prompt")
	h.enter()
	h.pump(func() bool { return !h.m.turn.running })
	h.keys("!ls -l")
	h.enter()
	h.pump(func() bool { return !h.m.turn.running })

	got := loadPromptHistory(h.m.hist.file)
	want := []histEntry{{Text: "first prompt"}, {Text: "ls -l", Bash: true}}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("history file = %+v", got)
	}
	// A fresh model in the same directory starts with them.
	m2 := newModel(Options{Agent: h.m.opts.Agent, Session: h.m.opts.Session, Provider: "fake", Cwd: h.m.opts.Cwd, Version: "test"})
	if len(m2.hist.entries) != 2 || m2.hist.idx != 2 {
		t.Fatalf("reloaded history = %+v idx=%d", m2.hist.entries, m2.hist.idx)
	}
	// Up browses; the "!" comes back with the command; esc restores the draft.
	h.keys("draft")
	h.update(tea.KeyMsg{Type: tea.KeyUp})
	if got := h.m.input.Value(); got != "!ls -l" {
		t.Fatalf("recall = %q, want the bash prefix back", got)
	}
	h.update(tea.KeyMsg{Type: tea.KeyEsc})
	if got := h.m.input.Value(); got != "draft" {
		t.Fatalf("esc left %q, want the draft back", got)
	}
}

// TestSteerAndTakeBackQueuedMessages: text typed while a turn runs must
// never be out of reach — ↑ takes the last queued message back, ctrl+s
// hands it to the running turn, and esc gives everything back.
func TestSteerAndTakeBackQueuedMessages(t *testing.T) {
	block := make(chan struct{})
	h := newHarness(t, &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "ok"), StopReason: llm.StopEndTurn})
	h.m.opts.Agent.Provider = &heldOpen{until: block, inner: h.m.opts.Agent.Provider}
	h.keys("do the thing")
	h.enter()
	h.keys("also this")
	h.enter()
	if len(h.m.turn.queued) != 1 {
		t.Fatalf("queued = %v", h.m.turn.queued)
	}
	h.update(tea.KeyMsg{Type: tea.KeyUp})
	if h.m.input.Value() != "also this" || len(h.m.turn.queued) != 0 {
		t.Fatalf("up did not take the queued message back: %q %v", h.m.input.Value(), h.m.turn.queued)
	}
	// ctrl+s hands it to the running turn instead.
	h.update(tea.KeyMsg{Type: tea.KeyCtrlS})
	if h.m.input.Value() != "" {
		t.Fatalf("ctrl+s left the input alone: %q", h.m.input.Value())
	}
	if !strings.Contains(plain(h.m.View()), "steering") {
		t.Fatalf("no sign of the steering message:\n%s", plain(h.m.View()))
	}
	// Queue two more, then cancel: both come back, joined by blank lines.
	h.keys("one")
	h.enter()
	h.keys("two")
	h.enter()
	h.update(tea.KeyMsg{Type: tea.KeyEsc})
	if got := h.m.input.Value(); got != "one\n\ntwo" {
		t.Fatalf("cancel left %q", got)
	}
	close(block)
	h.pump(func() bool { return !h.m.turn.running && len(h.m.turn.queued) == 0 })
}

// ctrl+g moves the command running in the foreground into the jobs
// registry, and the transcript says which job it became.
func TestCtrlGPromotesARunningCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell")
	}
	h := newHarness(t)
	run := &tool.Running{}
	jobs := &tool.Jobs{Dir: t.TempDir()}
	defer jobs.KillAll()
	h.m.opts.Running = run

	done := make(chan struct{})
	go func() {
		defer close(done)
		b := tool.Bash{Root: t.TempDir(), Jobs: jobs, Running: run}
		in, _ := json.Marshal(map[string]any{"command": "echo start; sleep 30", "timeout_ms": 120000})
		_, _ = b.Run(context.Background(), in)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for len(run.List()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the command never registered as promotable")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// While a command is promotable the status line offers the key.
	h.m.turn.running = true
	if !strings.Contains(h.m.statusView(), "ctrl+g") {
		t.Errorf("the status line does not offer the key:\n%s", h.m.statusView())
	}
	h.update(tea.KeyMsg{Type: tea.KeyCtrlG})
	if !strings.Contains(h.m.status, "moved to background as job 1") {
		t.Fatalf("the status line does not name the job: %q", h.m.status)
	}
	for _, e := range h.m.transcript.entries {
		if strings.Contains(e.text, "moved to background") {
			t.Fatalf("promotion leaked into the transcript: %q", e.text)
		}
	}
	if len(jobs.List()) != 1 {
		t.Fatalf("jobs = %+v", jobs.List())
	}
	<-done
	jobs.KillAll()
	// With nothing running the key says so instead of doing nothing
	// quietly, and the status line stops advertising it.
	h.update(tea.KeyMsg{Type: tea.KeyCtrlG})
	if !strings.Contains(h.m.status, "no command is running") {
		t.Errorf("status = %q", h.m.status)
	}
	if strings.Contains(h.m.statusView(), "ctrl+g") {
		t.Errorf("the status line still offers the key:\n%s", h.m.statusView())
	}
}

// Bubbletea splits typed text at spaces and hands each run over as one
// KeyRunes message whose String() is the word itself, and bubbles'
// key.Matches compares nothing else. Typing "move it right" therefore
// lost the word "right", "up" pulled a history entry into the middle of
// a sentence, and "home" jumped the cursor to the start of the line.
func TestTypedWordsAreNotKeyPresses(t *testing.T) {
	h := newHarness(t)
	h.m.hist.remember("first prompt")
	for _, word := range []string{"right", "left", "up", "down", "home", "end", "delete", "esc", "enter", "tab", "insert", "pgup"} {
		h.m.input.Reset()
		h.typed("aa " + word + " bb")
		if got := h.m.input.Value(); got != "aa "+word+" bb" {
			t.Errorf("typing %q gave %q", "aa "+word+" bb", got)
		}
	}
	// Real key presses still are key presses.
	h.m.input.Reset()
	h.update(tea.KeyMsg{Type: tea.KeyUp})
	if h.m.input.Value() != "first prompt" {
		t.Fatalf("up no longer browses history: %q", h.m.input.Value())
	}
}

// The same collision in the searchable modals: typing "end" in the
// session or model search used to page the highlight instead.
func TestTypedWordsInPickerSearch(t *testing.T) {
	h := newHarness(t)
	h.m.openPicker(&picker{
		title: "Pick",
		rows: []row{{key: "alpha", cells: []string{"alpha"}, name: "alpha"},
			{key: "friend", cells: []string{"friend"}, name: "friend"}},
		choose: func(r row, typed string) tea.Cmd { return nil },
	})
	h.typed("friend")
	if got := h.m.picker.field.Value(); got != "friend" {
		t.Fatalf("picker search took %q", got)
	}
	h.typed(" end")
	if got := h.m.picker.field.Value(); got != "friend end" {
		t.Fatalf("picker search took %q", got)
	}
}

func TestModeCycleShiftTab(t *testing.T) {
	h := newHarness(t)
	shiftTab := tea.KeyMsg{Type: tea.KeyShiftTab}
	if h.m.opts.Agent.PlanMode || h.m.opts.Agent.AutoApprove {
		t.Fatal("should start in ask")
	}
	if st := plain(h.m.statusView()); !strings.Contains(st, "ask") {
		t.Fatalf("idle status missing ask:\n%s", st)
	}
	h.update(shiftTab)
	if !h.m.opts.Agent.PlanMode || !strings.Contains(plain(h.m.statusView()), "PLAN") {
		t.Fatalf("ask→plan: plan=%v status:\n%s", h.m.opts.Agent.PlanMode, plain(h.m.statusView()))
	}
	h.update(shiftTab)
	if h.m.opts.Agent.PlanMode || h.m.opts.Agent.AutoApprove || !h.m.opts.Agent.AutoMode || !strings.Contains(plain(h.m.statusView()), "AUTO") {
		t.Fatalf("plan→auto: plan=%v auto=%v yolo=%v status:\n%s", h.m.opts.Agent.PlanMode, h.m.opts.Agent.AutoMode, h.m.opts.Agent.AutoApprove, plain(h.m.statusView()))
	}
	h.update(shiftTab)
	if h.m.opts.Agent.AutoMode || !h.m.opts.Agent.AutoApprove || !strings.Contains(plain(h.m.statusView()), "YOLO") {
		t.Fatalf("auto→yolo: auto=%v yolo=%v status:\n%s", h.m.opts.Agent.AutoMode, h.m.opts.Agent.AutoApprove, plain(h.m.statusView()))
	}
	h.update(shiftTab)
	if h.m.opts.Agent.PlanMode || h.m.opts.Agent.AutoApprove || h.m.opts.Agent.AutoMode {
		t.Fatalf("yolo→ask: plan=%v yolo=%v auto=%v", h.m.opts.Agent.PlanMode, h.m.opts.Agent.AutoApprove, h.m.opts.Agent.AutoMode)
	}
	if v := plain(h.m.View()); strings.Contains(v, "shift+tab cycles") {
		t.Fatalf("mode change dumped into the transcript:\n%s", v)
	}
}

func TestShiftTabInAskDoesNotCycleMode(t *testing.T) {
	h := newHarness(t)
	qs := []agent.Question{
		{Prompt: "One?", Options: []agent.AskOption{{Label: "A"}}},
		{Prompt: "Two?", Options: []agent.AskOption{{Label: "B"}}},
	}
	errc := make(chan error, 1)
	go func() {
		_, err := h.m.opts.Agent.Asker.Ask(context.Background(), qs)
		errc <- err
	}()
	h.pump(func() bool { return h.m.asking != nil })
	h.update(tea.KeyMsg{Type: tea.KeyShiftTab})
	if h.m.opts.Agent.PlanMode || h.m.opts.Agent.AutoApprove {
		t.Fatal("shift+tab in the ask overlay must not cycle the session mode")
	}
	if h.m.asking == nil || h.m.asking.tab != 1 {
		t.Fatalf("should move to the previous/next tab, tab=%d", h.m.asking.tab)
	}
	h.update(tea.KeyMsg{Type: tea.KeyEsc})
	select {
	case <-errc:
	case <-time.After(2 * time.Second):
		t.Fatal("ask did not finish")
	}
}

func TestCtrlTOpensAgentsOverlay(t *testing.T) {
	h := newHarness(t)
	attachTask(h)
	h.update(tea.KeyMsg{Type: tea.KeyCtrlT})
	if h.m.ov.kind != overlayAgents {
		t.Fatalf("overlay = %q, want %s", h.m.ov.kind, overlayAgents)
	}
	if v := plain(h.m.View()); !strings.Contains(v, "Subagents") {
		t.Fatalf("view:\n%s", v)
	}
}

// TestTheBoxNamesBothQueueingAndSteering: enter and ctrl+s are one key
// apart and do opposite things with the same text — after this turn, or
// into it. The placeholder has to say so, and has to go on saying both
// when the terminal is too narrow for the sentence.
func TestTheBoxNamesBothQueueingAndSteering(t *testing.T) {
	h := newHarness(t)
	h.m.turn.running = true
	for _, width := range []int{110, 80, 50, 34} {
		h.m.input.SetWidth(width)
		got := h.m.placeholder()
		if lipgloss.Width(got) > width-2 {
			t.Errorf("at %d columns the placeholder overflows: %q", width, got)
		}
		if !strings.Contains(got, "enter queues") || !strings.Contains(got, "ctrl+s steers") {
			t.Errorf("at %d columns the placeholder names only one of the two: %q", width, got)
		}
	}
	// The wordings really do differ, so the narrow ones are a fit and not
	// the same sentence cut short.
	h.m.input.SetWidth(110)
	wide := h.m.placeholder()
	h.m.input.SetWidth(34)
	if narrow := h.m.placeholder(); narrow == wide {
		t.Fatalf("the placeholder did not give way at 34 columns: %q", narrow)
	}
}

// TestQueueingSaysHowToSteerInstead: pressing enter during a turn puts
// the message somewhere it will sit until the turn ends. Both places
// that say so — the status line at the moment it happens, and the
// reserved row for as long as it waits — name the other key.
func TestQueueingSaysHowToSteerInstead(t *testing.T) {
	h := newHarness(t)
	h.m.turn.running = true
	h.keys("look at the other file too")
	h.enter()
	if len(h.m.turn.queued) != 1 {
		t.Fatalf("queued = %v", h.m.turn.queued)
	}
	for _, want := range []string{"queued 1", "sends when this turn ends", "ctrl+s"} {
		if !strings.Contains(h.m.status, want) {
			t.Errorf("the queue note does not mention %q: %q", want, h.m.status)
		}
	}
	h.m.width = 200
	row := plain(h.m.footerNotice())
	for _, want := range []string{"queued 1", "look at the other file too", "up takes it back", "ctrl+s steers it into this turn"} {
		if !strings.Contains(row, want) {
			t.Errorf("the reserved row does not mention %q: %q", want, row)
		}
	}
	// At 80 columns a long message is clipped, not the keys.
	h.m.turn.queued = []string{strings.Repeat("please also rename every helper in the package ", 4)}
	h.m.width = 80
	row = plain(h.m.footerNotice())
	for _, want := range []string{"queued 1", "please al", "↑ takes back", "ctrl+s steers"} {
		if !strings.Contains(row, want) {
			t.Errorf("80 columns: the reserved row does not mention %q: %q", want, row)
		}
	}
}

// TestTheFirstQueuedMessageTeachesCtrlS: the hint engine says it once
// per project, and never to somebody who already steers.
func TestTheFirstQueuedMessageTeachesCtrlS(t *testing.T) {
	queue := func(h *harness, text string) {
		h.m.turn.running = true
		h.keys(text)
		h.enter()
	}
	tips := func(h *harness) []string {
		var out []string
		for _, e := range h.m.transcript.entries {
			if e.kind == entryTip {
				out = append(out, e.text)
			}
		}
		return out
	}

	h := newHarness(t)
	h.m.hint.engine = hints.New(nil, nil)
	queue(h, "one")
	queue(h, "two")
	got := tips(h)
	if len(got) != 1 || !strings.Contains(got[0], "ctrl+s") {
		t.Fatalf("the first queued message should teach ctrl+s exactly once: %q", got)
	}

	// Somebody who has already steered is told nothing.
	h2 := newHarness(t)
	h2.m.hint.engine = hints.New(nil, nil)
	h2.m.opts.Usage = feature.OpenUsage(t.TempDir())
	h2.m.noteUse("ctrl+s")
	queue(h2, "one")
	if got := tips(h2); len(got) != 0 {
		t.Fatalf("taught ctrl+s to a user who already steers: %q", got)
	}
}
