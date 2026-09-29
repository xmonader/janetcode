package tui

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/keymap"
	"github.com/xmonader/janetcode/internal/llm"
)

func okResp() *llm.Response {
	return &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "ok"), StopReason: llm.StopEndTurn}
}

var (
	ctrlR = tea.KeyMsg{Type: tea.KeyCtrlR}
	esc   = tea.KeyMsg{Type: tea.KeyEsc}
)

// Every action keymap knows has a handler here, and no handler is for an
// action keymap does not know: a bound key with no handler would be a
// nil call on the next press.
func TestEveryActionHasAHandler(t *testing.T) {
	for _, d := range keymap.Defs {
		if actionKeys[d.Action] == nil {
			t.Errorf("action %s has no handler", d.Action)
		}
	}
	if len(actionKeys) != len(keymap.Defs) {
		t.Errorf("actionKeys has %d entries for %d actions", len(actionKeys), len(keymap.Defs))
	}
}

func TestHistorySearch(t *testing.T) {
	h := newHarness(t, okResp())
	for _, p := range []string{"run the tests", "fix the parser bug", "explain main.go", "run the linter"} {
		h.m.hist.remember(p)
	}
	h.keys("draft")
	h.update(ctrlR)
	if h.m.mode() != modeSearch {
		t.Fatalf("ctrl+r did not open the search (mode %s)", h.m.mode())
	}
	h.keys("run")
	if v := h.m.input.Value(); v != "run the linter" {
		t.Fatalf("newest match = %q", v)
	}
	if s := plain(h.m.statusView()); !strings.Contains(s, "reverse-i-search)`run'") {
		t.Fatalf("status does not show the search:\n%s", s)
	}
	h.update(ctrlR)
	if v := h.m.input.Value(); v != "run the tests" {
		t.Fatalf("ctrl+r again = %q, want the older match", v)
	}
	h.update(ctrlR) // nothing older: stays, marked failing
	if v := h.m.input.Value(); v != "run the tests" || !h.m.hsearch.fail {
		t.Fatalf("past the oldest match: %q fail=%v", v, h.m.hsearch.fail)
	}
	// enter keeps the match in the composer and sends nothing.
	h.enter()
	if h.m.hsearch != nil || h.m.input.Value() != "run the tests" || h.m.turn.running {
		t.Fatalf("enter: search=%v input=%q running=%v", h.m.hsearch != nil, h.m.input.Value(), h.m.turn.running)
	}

	// esc puts back what was being typed.
	h.inputReset()
	h.keys("half typed")
	h.update(ctrlR)
	h.keys("parser")
	if v := h.m.input.Value(); v != "fix the parser bug" {
		t.Fatalf("match = %q", v)
	}
	h.update(esc)
	if h.m.hsearch != nil || h.m.input.Value() != "half typed" {
		t.Fatalf("esc: input = %q", h.m.input.Value())
	}

	// Any other key keeps the match and then does its own thing.
	h.inputReset()
	h.update(ctrlR)
	h.keys("explain")
	h.update(tea.KeyMsg{Type: tea.KeyLeft})
	if h.m.hsearch != nil || h.m.input.Value() != "explain main.go" {
		t.Fatalf("left: search open=%v input=%q", h.m.hsearch != nil, h.m.input.Value())
	}
	h.keys("X")
	if v := h.m.input.Value(); v != "explain main.gXo" {
		t.Fatalf("the arrow was not applied after the search closed: %q", v)
	}
}

func TestExternalEditor(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake editor is a shell script")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "ed.sh")
	// The editor appends a line to what it was given, as a person would.
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf ' and more\\nsecond line\\n' >> \"$1\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VISUAL", script)
	h := newHarness(t, okResp())
	var ran *exec.Cmd
	h.m.runEditor = func(c *exec.Cmd, fn tea.ExecCallback) tea.Cmd {
		ran = c
		err := c.Run()
		return func() tea.Msg { return fn(err) }
	}
	h.keys("first")
	h.updateCmd(tea.KeyMsg{Type: tea.KeyCtrlX})
	if h.m.chord != "ctrl+x" {
		t.Fatalf("ctrl+x did not wait for the chord's second key")
	}
	h.updateCmd(tea.KeyMsg{Type: tea.KeyCtrlE})
	if ran == nil {
		t.Fatal("the editor did not run")
	}
	if v := h.m.input.Value(); v != "first and more\nsecond line" {
		t.Fatalf("composer = %q", v)
	}
	if h.m.turn.running {
		t.Fatal("the edited prompt was sent")
	}
	if _, err := os.Stat(ran.Args[len(ran.Args)-1]); !os.IsNotExist(err) {
		t.Fatalf("the temp file was left behind: %v", err)
	}

	// A failing editor leaves the prompt alone.
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho junk > \"$1\"; exit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	h.updateCmd(tea.KeyMsg{Type: tea.KeyCtrlX})
	h.updateCmd(tea.KeyMsg{Type: tea.KeyCtrlE})
	if v := h.m.input.Value(); v != "first and more\nsecond line" {
		t.Fatalf("a failed edit changed the prompt: %q", v)
	}
}

func TestEditorCommand(t *testing.T) {
	env := map[string]string{"EDITOR": "code --wait"}
	get := func(k string) string { return env[k] }
	if got := editorCommand(get); strings.Join(got, " ") != "code --wait" {
		t.Fatalf("EDITOR = %v", got)
	}
	env["VISUAL"] = "nvim"
	if got := editorCommand(get); got[0] != "nvim" {
		t.Fatalf("VISUAL should win: %v", got)
	}
}

// A chord's first key followed by a key that completes nothing is
// swallowed and said, not typed.
func TestUnboundChordIsNotTyped(t *testing.T) {
	h := newHarness(t, okResp())
	h.update(tea.KeyMsg{Type: tea.KeyCtrlX})
	h.keys("q")
	if v := h.m.input.Value(); v != "" || h.m.chord != "" {
		t.Fatalf("input %q chord %q", v, h.m.chord)
	}
	if !strings.Contains(h.m.status, "ctrl+x q is not bound") {
		t.Fatalf("status = %q", h.m.status)
	}
}

func harnessWithKeys(t *testing.T, bindings string, vim bool) *harness {
	t.Helper()
	var ov keymap.Overrides
	if err := json.Unmarshal([]byte(bindings), &ov); err != nil {
		t.Fatal(err)
	}
	km, err := keymap.Resolve(ov)
	if err != nil {
		t.Fatal(err)
	}
	opts := fakeOptions(t, okResp())
	opts.Config = &config.Config{KeyMap: km, VimMode: vim}
	return newHarnessFor(t, opts)
}

func TestKeybindingsRebind(t *testing.T) {
	h := harnessWithKeys(t, `{"effort": "f2", "history_search": "alt+r", "newline": "alt+n"}`, false)
	h.m.opts.Agent.Effort = "xhigh"
	h.update(tea.KeyMsg{Type: tea.KeyF2})
	if got := h.m.opts.Agent.Settings().Effort; got != "max" {
		t.Fatalf("f2 did not run effort: %q", got)
	}
	h.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}, Alt: true})
	if got := h.m.opts.Agent.Settings().Effort; got != "max" {
		t.Fatalf("alt+e still cycles effort after it was moved: %q", got)
	}
	h.m.hist.remember("an old prompt")
	h.update(ctrlR)
	if h.m.hsearch != nil {
		t.Fatal("ctrl+r still searches after history_search moved")
	}
	h.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}, Alt: true})
	if h.m.hsearch == nil {
		t.Fatal("alt+r does not search")
	}
	h.update(esc)
	h.inputReset()
	h.keys("a")
	h.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}, Alt: true})
	h.keys("b")
	if v := h.m.input.Value(); v != "a\nb" {
		t.Fatalf("alt+n newline: %q", v)
	}
	h.update(tea.KeyMsg{Type: tea.KeyCtrlJ})
	if v := h.m.input.Value(); v != "a\nb" {
		t.Fatalf("ctrl+j still inserts a newline after newline moved: %q", v)
	}
	help := h.m.keysHelpText()
	if !strings.Contains(help, "effort           f2") || !strings.Contains(help, "yours: newline, effort, history_search") {
		t.Fatalf("/help keys does not show the bindings in force:\n%s", help)
	}
}

// vimKeys sends normal-mode commands one character at a time.
func vimKeys(h *harness, s string) {
	h.keys(s)
}

func TestVimMode(t *testing.T) {
	h := harnessWithKeys(t, `{}`, true)
	h.keys("hello world foo")
	if chip := plain(h.m.statusView()); !strings.Contains(chip, "INSERT") {
		t.Fatalf("no INSERT indicator:\n%s", chip)
	}
	h.update(esc)
	if !h.m.vim.normal {
		t.Fatal("esc did not enter normal mode")
	}
	if chip := plain(h.m.statusView()); !strings.Contains(chip, "NORMAL") {
		t.Fatalf("no NORMAL indicator:\n%s", chip)
	}
	// esc in normal mode must not clear the prompt.
	h.update(esc)
	if v := h.m.input.Value(); v != "hello world foo" {
		t.Fatalf("esc in normal mode changed the input: %q", v)
	}
	vimKeys(h, "0w")
	vimKeys(h, "cw")
	if h.m.vim.normal {
		t.Fatal("cw did not enter insert mode")
	}
	h.keys("there")
	h.update(esc)
	if v := h.m.input.Value(); v != "hello there foo" {
		t.Fatalf("cw: %q", v)
	}
	vimKeys(h, "0x")
	if v := h.m.input.Value(); v != "ello there foo" {
		t.Fatalf("x: %q", v)
	}
	vimKeys(h, "u")
	if v := h.m.input.Value(); v != "hello there foo" {
		t.Fatalf("u: %q", v)
	}
	vimKeys(h, "$b")
	vimKeys(h, "ciw")
	h.keys("bar")
	h.update(esc)
	if v := h.m.input.Value(); v != "hello there bar" {
		t.Fatalf("ciw: %q", v)
	}
	vimKeys(h, "o")
	h.keys("line two")
	h.update(esc)
	if v := h.m.input.Value(); v != "hello there bar\nline two" {
		t.Fatalf("o: %q", v)
	}
	vimKeys(h, "kdd")
	if v := h.m.input.Value(); v != "line two" {
		t.Fatalf("dd: %q", v)
	}
	vimKeys(h, "p")
	if v := h.m.input.Value(); v != "line two\nhello there bar" {
		t.Fatalf("p after dd: %q", v)
	}
	vimKeys(h, "ggdw")
	if v := h.m.input.Value(); v != "two\nhello there bar" {
		t.Fatalf("gg dw: %q", v)
	}
	vimKeys(h, "A")
	h.keys("!")
	if v := h.m.input.Value(); v != "two!\nhello there bar" {
		t.Fatalf("A: %q", v)
	}
	// enter sends from either mode.
	h.update(esc)
	h.enter()
	if !h.m.turn.running && h.m.input.Value() != "" {
		t.Fatalf("enter in normal mode did not send: %q", h.m.input.Value())
	}
}

func TestVimToggleCommand(t *testing.T) {
	h := newHarness(t, okResp())
	h.keys("/vim")
	h.enter()
	if !h.m.vim.on {
		t.Fatal("/vim did not turn vim mode on")
	}
	h.keys("abc")
	h.update(esc)
	vimKeys(h, "x")
	if v := h.m.input.Value(); v != "ab" {
		t.Fatalf("x after /vim: %q", v)
	}
	vimKeys(h, "cc")
	h.keys("/vim off")
	h.enter()
	if h.m.vim.on {
		t.Fatal("/vim off left it on")
	}
	// Off, esc clears as it always has.
	h.keys("xyz")
	h.update(esc)
	if v := h.m.input.Value(); v != "" {
		t.Fatalf("esc with vim off: %q", v)
	}
	// /config vim_mode=true saves it and turns it on now.
	h.withConfig(t, &config.Config{}, nil)
	h.keys("/config vim_mode=true")
	h.enter()
	if !h.m.vim.on || !h.m.opts.Config.VimMode {
		t.Fatalf("/config vim_mode=true: on=%v cfg=%v status=%q", h.m.vim.on, h.m.opts.Config.VimMode, h.m.status)
	}
}

func TestWordMotions(t *testing.T) {
	text := []rune("foo.bar  baz\nqux")
	if got := wordForward(text, 0); got != 3 {
		t.Errorf("w from foo = %d", got)
	}
	if got := wordForward(text, 4); got != 9 {
		t.Errorf("w from bar = %d", got)
	}
	if got := wordForward(text, 9); got != 13 {
		t.Errorf("w across the newline = %d", got)
	}
	if got := wordBack(text, 9); got != 4 {
		t.Errorf("b from baz = %d", got)
	}
	if got := wordEnd(text, 0); got != 2 {
		t.Errorf("e from foo = %d", got)
	}
	if s, e := innerWord(text, 5); s != 4 || e != 7 {
		t.Errorf("iw on bar = %d,%d", s, e)
	}
}
