package tui

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/textfmt"
	"github.com/xmonader/janetcode/internal/theme"
)

func TestTailPath(t *testing.T) {
	if got := tailPath("/a/b", 20); got != "/a/b" {
		t.Fatalf("short: %q", got)
	}
	got := tailPath("/very/long/path/to/some/project/dir", 16)
	if len([]rune(got)) > 16 || !strings.HasPrefix(got, "…/") || !strings.HasSuffix(got, "/dir") {
		t.Fatalf("long: %q", got)
	}
	// A path of wide characters is wider than the budget while having
	// fewer runes than it: the cut is in runes and the fit is in
	// columns, which used to index before the start of the slice.
	for _, p := range []string{
		"/" + strings.Repeat("日", 12),
		strings.Repeat("本", 30) + "/" + strings.Repeat("語", 4),
		"日本語",
	} {
		if got := tailPath(p, 12); got == "" {
			t.Fatalf("tailPath(%q) = %q", p, got)
		}
	}
}

// cutWords searches for a word boundary; the search has to count runes,
// because a byte index used as a rune index either cuts a non-Latin
// thought far too short or slices past the end of it and panics.
func TestCutWordsCountsRunes(t *testing.T) {
	if got := cutWords("one two three four five six seven", 20); got != "one two three four…" {
		t.Fatalf("ascii: %q", got)
	}
	long := strings.Repeat("あ", 60) + " " + strings.Repeat("い", 60)
	got := cutWords(long, 100)
	if r := []rune(got); len(r) > 100 {
		t.Fatalf("cut to %d runes: %q", len(r), got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("no cut mark: %q", got)
	}
	// The boundary is the space at rune 60, which is past max/2.
	if want := strings.Repeat("あ", 60) + "…"; got != want {
		t.Fatalf("cut at the wrong place: %q", got)
	}
	if got := cutWords("abc", 0); got != "" {
		t.Fatalf("max 0: %q", got)
	}
}

func TestSplitMarkdownBlocks(t *testing.T) {
	got := splitMarkdownBlocks("a\n\nb\n```\nx\n\ny\n```\n\nc")
	if len(got) != 3 || got[1] != "b\n```\nx\n\ny\n```" || got[2] != "c" {
		t.Fatalf("got %q", got)
	}
}

func TestFooterShowsCacheRate(t *testing.T) {
	h := newHarness(t, &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "hi"), StopReason: llm.StopEndTurn, Usage: llm.Usage{InputTokens: 100, CacheReadTokens: 900}})
	if strings.Contains(plain(h.m.View()), "cache ") {
		t.Fatal("cache rate shown before any call")
	}
	h.keys("hello")
	h.enter()
	h.pump(func() bool { return !h.m.turn.running })
	if v := plain(h.m.View()); !strings.Contains(v, "cache 90%") {
		t.Fatalf("view:\n%s", v)
	}
	h.keys("/reload")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "rebuilt for the next call") {
		t.Fatalf("view:\n%s", v)
	}
}

// TestFitPathKeepsTheTail: a path that does not fit loses leading
// components, not the middle of a directory name.
func TestFitPathKeepsTheTail(t *testing.T) {
	p := "/tmp/claude-1000/dd12e125-65a6-415a-b19b-31de5e8c912b/scratchpad/tuiproj"
	if got := fitPath(p, 40); got != "…/scratchpad/tuiproj" {
		t.Fatalf("got %q", got)
	}
	if got := fitPath(p, 12); got != "…/tuiproj" {
		t.Fatalf("got %q", got)
	}
	if got := fitPath("/a/b", 40); got != "/a/b" {
		t.Fatalf("fits unchanged: %q", got)
	}
}

// TestViewportPinning covers item 2 of the polish round: streaming output
// follows the bottom until the user scrolls away, a resize mid-stream does
// not yank them back, the "pgdn for latest" hint tracks that state, and a
// new prompt re-pins.
func TestViewportPinning(t *testing.T) {
	h := newHarness(t,
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "first"), StopReason: llm.StopEndTurn},
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "second"), StopReason: llm.StopEndTurn},
	)
	h.update(tea.WindowSizeMsg{Width: 80, Height: 12})
	for i := 0; i < 60; i++ {
		h.m.transcript.info(fmt.Sprintf("filler line %d", i))
	}
	h.m.refresh(false)
	if !h.m.pinned || !h.m.viewport.AtBottom() {
		t.Fatal("a fresh transcript must follow the bottom")
	}
	if strings.Contains(plain(h.m.statusView()), "pgdn for latest") {
		t.Fatal("hint shown while pinned")
	}
	// Scrolling up unpins and raises the hint.
	h.update(tea.KeyMsg{Type: tea.KeyPgUp})
	if h.m.pinned {
		t.Fatal("pgup must unpin")
	}
	if !strings.Contains(plain(h.m.statusView()), "pgdn for latest") {
		t.Fatal("hint missing after scrolling up")
	}
	// New output while scrolled up must not move the view...
	off := h.m.viewport.YOffset
	h.m.transcript.info("streamed while scrolled up")
	h.m.refresh(false)
	if h.m.viewport.YOffset != off {
		t.Fatalf("output yanked the view: offset %d → %d", off, h.m.viewport.YOffset)
	}
	// ...and neither must a resize.
	h.update(tea.WindowSizeMsg{Width: 80, Height: 14})
	if h.m.pinned {
		t.Fatalf("resize re-pinned a scrolled-up view (offset %d)", h.m.viewport.YOffset)
	}
	// Scrolling back to the bottom re-pins.
	for i := 0; i < 30; i++ {
		h.update(tea.KeyMsg{Type: tea.KeyPgDown})
	}
	if !h.m.pinned {
		t.Fatal("reaching the bottom must re-pin")
	}
	// A prompt sent while scrolled up re-pins too.
	h.update(tea.KeyMsg{Type: tea.KeyPgUp})
	if h.m.pinned {
		t.Fatal("pgup must unpin")
	}
	h.keys("hi")
	h.enter()
	if !h.m.pinned {
		t.Fatal("a new prompt must re-pin")
	}
	h.pump(func() bool { return !h.m.turn.running })
}

// TestInputBoxStates covers item 5: the border colour and the placeholder
// must agree about whose turn it is. Idle and the connect dialog are
// accent, a running turn is dim, and a denial reason is warn like the
// permission box it answers.
func TestInputBoxStates(t *testing.T) {
	h := newHarness(t,
		&llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ID: "w1", Name: "write_file", Input: json.RawMessage(`{"path":"n.txt","content":"x\n"}`)}}}, StopReason: llm.StopToolUse},
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "ok"), StopReason: llm.StopEndTurn},
	)
	check := func(what string, want lipgloss.Style, wantHint string) {
		t.Helper()
		if got := h.m.inputBox().GetBorderTopForeground(); got != want.GetBorderTopForeground() {
			t.Errorf("%s: border = %v, want %v", what, got, want.GetBorderTopForeground())
		}
		if !strings.Contains(h.m.placeholder(), wantHint) {
			t.Errorf("%s: placeholder = %q, want it to mention %q", what, h.m.placeholder(), wantHint)
		}
	}
	check("idle", h.m.styles.boxIdle, defaultPlaceholder)
	h.keys("make n.txt")
	h.enter()
	h.pump(func() bool { return h.m.pending != nil })
	check("permission", h.m.styles.boxWarn, "this session")
	if h.m.styles.permBox.GetBorderTopForeground() != h.m.styles.boxWarn.GetBorderTopForeground() {
		t.Error("the permission box does not match the warning palette")
	}
	h.keys("n")
	if !h.m.denying {
		t.Fatal("n opens the reason prompt")
	}
	check("denial reason", h.m.styles.boxWarn, "Why?")
	h.enter()
	h.pump(func() bool { return !h.m.turn.running })
	check("back to idle", h.m.styles.boxIdle, defaultPlaceholder)
	h.m.opts.Agent.SetPlanMode(true)
	check("plan mode", h.m.styles.boxIdle, "Plan mode")
	h.m.opts.Agent.SetPlanMode(false)
	h.m.turn.running = true
	check("running", h.m.styles.boxBusy, "queue")
	h.m.turn.running = false
	// The connect dialog is another box you type into: same accent border.
	if h.m.styles.dialog.GetBorderTopForeground() != h.m.styles.boxIdle.GetBorderTopForeground() {
		t.Error("the connect dialog border does not match an idle input box")
	}
}

// TestStatusBarFitsWidth covers item 6: at 80 columns nothing on the
// status line may overflow, in any state. The status text yields; the
// context gauge never does.
func TestStatusBarFitsWidth(t *testing.T) {
	h := newHarness(t, &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "x"), StopReason: llm.StopEndTurn})
	states := []struct {
		name string
		set  func()
	}{
		{"idle", func() { h.m.turn.running, h.m.pending, h.m.status = false, nil, "" }},
		{"running with a long phase", func() {
			h.m.turn.running = true
			h.m.turn.startedAt = time.Now().Add(-90 * time.Second)
			h.m.turn.phase = "write_file scheme.py · writing 754 lines (21.3KB) so far"
		}},
		{"approval needed", func() {
			h.m.turn.running = false
			h.m.pending = &permissionMsg{call: agent.ToolCall{Name: "bash"}}
		}},
		{"done with a verdict", func() {
			h.m.pending = nil
			h.m.status = "✓ verified · done in 12.4s · $0.02 · 3 files changed · /undo reverts · in 12k · out 3.1k"
		}},
		{"scrolled up", func() { h.m.pinned = false }},
	}
	for _, w := range []int{120, 80, 60, 40, 24} {
		h.update(tea.WindowSizeMsg{Width: w, Height: 24})
		for _, st := range states {
			st.set()
			line := h.m.statusView()
			if got := lipgloss.Width(line); got > w {
				t.Errorf("width %d, %s: status line is %d columns:\n%s", w, st.name, got, plain(line))
			}
			// The gauge is the one thing that never yields — not even its
			// last characters: a figure cut to "45…" reads as a number.
			// Which of its wordings is drawn depends on the room left,
			// but one of them is always there whole.
			if !gaugeWhole(h.m, line) {
				t.Errorf("width %d, %s: no gauge wording survived whole:\n%s", w, st.name, plain(line))
			}
		}
		h.m.pinned = true
	}
	// At 80 columns with the scroll hint up, the hint survives and the
	// status text is the part that gives way.
	h.update(tea.WindowSizeMsg{Width: 80, Height: 24})
	h.m.pinned = false
	h.m.status = strings.Repeat("a very long status ", 6)
	line := plain(h.m.statusView())
	if !strings.Contains(line, "pgdn for latest") || !strings.Contains(line, "…") {
		t.Errorf("at 80 columns the hint should stay and the text give way:\n%s", line)
	}
	h.m.pinned = true
}

// TestHeaderNarrowWidths covers item 7: the header drops the session
// title first, then the branch, then shortens the path, and never wraps
// or spills past the width.
func TestHeaderNarrowWidths(t *testing.T) {
	h := newHarness(t, &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "x"), StopReason: llm.StopEndTurn})
	h.m.opts.Cwd = "/home/someone/work/projects/janetcode/internal/tui"
	h.m.branch = "feature/very-long-branch-name"
	h.m.opts.Session.Meta.Title = "polishing the terminal interface"
	h.m.opts.Agent.AutoApprove = true
	at := func(w int) string {
		h.update(tea.WindowSizeMsg{Width: w, Height: 24})
		line := h.m.headerView()
		if got := lipgloss.Width(line); got != w {
			t.Fatalf("width %d: header is %d columns:\n%s", w, got, plain(line))
		}
		if strings.Contains(line, "\n") {
			t.Fatalf("width %d: header wrapped:\n%s", w, plain(line))
		}
		return plain(line)
	}
	wide := at(120)
	for _, want := range []string{"janetcode", "polishing the terminal", "YOLO", "tui", "feature/very-long-branch-name"} {
		if !strings.Contains(wide, want) {
			t.Errorf("at 120 columns the header lost %q:\n%s", want, wide)
		}
	}
	// The title is the first thing to go, and the branch outlives it.
	narrow := at(90)
	if strings.Contains(narrow, "polishing") {
		t.Errorf("at 90 columns the title should be dropped first:\n%s", narrow)
	}
	if !strings.Contains(narrow, "feature/very-long-branch-name") {
		t.Errorf("at 90 columns the branch should still be whole:\n%s", narrow)
	}
	// Then the branch goes, whole rather than cut in half.
	narrower := at(60)
	if strings.Contains(narrower, "⎇") || strings.Contains(narrower, "feature/very-") {
		t.Errorf("at 60 columns the branch should be dropped, not clipped:\n%s", narrower)
	}
	if !strings.Contains(narrower, "tui") {
		t.Errorf("at 60 columns the path should survive:\n%s", narrower)
	}
	// Then the path is shortened from the front, keeping the leaf.
	if got := at(40); !strings.Contains(got, "…/tui") {
		t.Errorf("at 40 columns the path should be shortened to its tail:\n%s", got)
	}
	// Below that, whatever is left still fits exactly.
	for _, w := range []int{30, 24, 20} {
		at(w)
	}
}

// TestErrorRendering covers item 9: a multi-line error is one colour with
// its continuation indented under the mark, and a provider failure
// mid-stream leaves no half-rendered assistant or tool block behind.
func TestErrorRendering(t *testing.T) {
	h := newHarness(t)
	h.m.opts.Agent.Provider = failMidStream{err: errors.New("Post \"https://api.example.com/v1/messages\": dial tcp 10.0.0.1:443: connect: connection refused")}
	h.update(tea.WindowSizeMsg{Width: 60, Height: 30})
	h.keys("do it")
	h.enter()
	h.pump(func() bool { return !h.m.turn.running })
	v := plain(h.m.View())
	if !strings.Contains(v, "✗ Cannot reach the API") {
		t.Fatalf("error not shown:\n%s", v)
	}
	// Every continuation line of the error is indented two columns under
	// the mark, including the ones wrapping made.
	var seen, indented int
	for _, l := range strings.Split(v, "\n") {
		switch {
		case strings.HasPrefix(l, "✗ Cannot reach the API"):
			seen++
		case strings.Contains(l, "Check the base URL") || strings.Contains(l, "nothing is listening") || strings.Contains(l, "your network"):
			seen++
			if strings.HasPrefix(l, "  ") && !strings.HasPrefix(l, "   ") {
				indented++
			}
		}
	}
	if seen < 2 || indented == 0 {
		t.Errorf("error continuation lines are not indented under the mark:\n%s", v)
	}
	// Nothing is left streaming: no cursor, and the tool that never ran is
	// marked failed rather than spinning.
	if strings.Contains(v, "▍") {
		t.Errorf("assistant block left half-rendered:\n%s", v)
	}
	if !strings.Contains(v, "✗ write_file") {
		t.Errorf("the unfinished tool call is still open:\n%s", v)
	}
	for _, e := range h.m.transcript.entries {
		switch e.kind {
		case entryAssistant, entryThinking, entryTool:
			if !e.done {
				t.Errorf("a %v entry was left streaming after the error", e.kind)
			}
		}
	}
}

// TestThemeDetection covers item 10: an unanswered background query means
// dark, an explicit theme wins, and whatever is decided is pinned on
// lipgloss so the palette and the markdown style agree.
func TestThemeDetection(t *testing.T) {
	defer lipgloss.SetHasDarkBackground(lipgloss.HasDarkBackground())
	// A model built with a theme renders its markdown in that theme and
	// pins lipgloss to it, so the palette cannot drift the other way.
	h := newHarness(t)
	m2 := newModel(Options{Agent: h.m.opts.Agent, Session: h.m.opts.Session, Cwd: h.m.opts.Cwd, Theme: "light"})
	if m2.transcript.styleName != "light" || lipgloss.HasDarkBackground() {
		t.Errorf("model theme=%q, lipgloss dark=%v: the palette and the markdown style disagree",
			m2.transcript.styleName, lipgloss.HasDarkBackground())
	}
}

// TestLightPaletteContrast covers the other half of item 10: faint text on
// a white background was at 2.5:1, which is barely visible.
func TestLightPaletteContrast(t *testing.T) {
	for _, c := range []struct {
		name string
		col  lipgloss.AdaptiveColor
		min  float64
	}{
		{"faint", defaultPalette.faint, 3.5}, {"dim", defaultPalette.dim, 4.0}, {"accent", defaultPalette.accent, 4.0},
		{"warn", defaultPalette.warn, 4.0}, {"err", defaultPalette.err, 4.0}, {"ok", defaultPalette.ok, 4.0},
		{"user", defaultPalette.user, 4.0}, {"tool", defaultPalette.tool, 4.0}, {"border", defaultPalette.border, 2.5},
	} {
		if got := contrast(c.col.Light, "#ffffff"); got < c.min {
			t.Errorf("light %s (%s) on white is %.2f:1, want at least %.1f", c.name, c.col.Light, got, c.min)
		}
		if got := contrast(c.col.Dark, "#1e1e1e"); got < c.min-1 {
			t.Errorf("dark %s (%s) on #1e1e1e is %.2f:1, want at least %.1f", c.name, c.col.Dark, got, c.min-1)
		}
	}
}

// TestUserVisibleStringStyle covers item 14: the strings the interface
// shows follow one style — sentence case, no shouting outside the header's
// mode prefixes, "·" between clauses rather than a run of spaces, and
// plural() rather than "(s)".
func TestUserVisibleStringStyle(t *testing.T) {
	// Acronyms and the header's mode prefixes are the only capitals.
	allowed := map[string]bool{
		"MCP": true, "API": true, "URL": true, "HEAD": true, "JSON": true,
		"YOLO": true, "AUTO": true, "EDITS": true, "PLAN": true, "READ-ONLY": true, "OK": true,
	}
	caps := regexp.MustCompile(`\b[A-Z][A-Z-]+\b`)
	check := func(what, s string) {
		t.Helper()
		if strings.Contains(s, "(s)") {
			t.Errorf("%s uses (s) instead of plural(): %q", what, s)
		}
		if strings.Contains(s, "  ") {
			t.Errorf("%s has a double space: %q", what, s)
		}
		if strings.HasSuffix(strings.TrimSpace(s), ".") {
			t.Errorf("%s ends with a full stop: %q", what, s)
		}
		for _, w := range caps.FindAllString(s, -1) {
			if !allowed[w] {
				t.Errorf("%s shouts %q: %q", what, w, s)
			}
		}
	}
	for _, c := range commands {
		check("the help for "+c.name, c.help)
		check("the arguments of "+c.name, c.args)
		if c.help != strings.ToLower(c.help[:1])+c.help[1:] && !allowed[strings.SplitN(c.help, " ", 2)[0]] {
			t.Errorf("the help for %s is not sentence case: %q", c.name, c.help)
		}
	}
	h := newHarness(t)
	check("the default placeholder", defaultPlaceholder)
	for _, set := range []func(){
		func() { h.m.turn.running = true },
		func() { h.m.turn.running, h.m.denying = false, true },
		func() { h.m.denying = false; h.m.opts.Agent.SetPlanMode(true) },
		func() { h.m.opts.Agent.SetPlanMode(false); h.m.opts.Agent.ReadOnly = true },
	} {
		set()
		check("a placeholder", h.m.placeholder())
	}
	h.m.opts.Agent.ReadOnly = false
	check("the welcome banner", h.m.welcome(""))
	check("the resumed banner", h.m.welcome(resumedLine("abc123", 2)))
	// Sizes, durations and token counts go through the shared helpers.
	if textfmt.Bytes(2048) != "2.0KB" || textfmt.Duration(1500*time.Millisecond) != "1.5s" || textfmt.Tokens(12000) != "12k" {
		t.Errorf("the shared formatters changed shape: %q %q %q",
			textfmt.Bytes(2048), textfmt.Duration(1500*time.Millisecond), textfmt.Tokens(12000))
	}
	if textfmt.Plural(1, "file") != "1 file" || textfmt.Plural(2, "file") != "2 files" {
		t.Errorf("Plural() = %q / %q", textfmt.Plural(1, "file"), textfmt.Plural(2, "file"))
	}
}

// TestTerminalIntegration covers the escape sequences the terminal needs
// to take part: OSC 133 zones around a turn, and a notification only
// while the window is unfocused.
func TestTerminalIntegration(t *testing.T) {
	var buf bytes.Buffer
	ti := &termIntegration{out: &buf, focused: true, notify: true}
	ti.turnStarted()
	ti.turnEnded(true, "done in 2s")
	if got := buf.String(); got != "\x1b]133;A\x07\x1b]133;B\x07\x1b]133;C\x07\x1b]133;D;0\x07" {
		t.Fatalf("zones = %q", got)
	}
	buf.Reset()
	ti.announce("janetcode", "approval needed: bash")
	if buf.Len() != 0 {
		t.Fatalf("a focused window was notified: %q", buf.String())
	}
	ti.focused = false
	ti.announce("janetcode", "approval needed: bash")
	if got := buf.String(); !strings.Contains(got, "\x1b]9;approval needed: bash\x07") || !strings.Contains(got, "\a") {
		t.Fatalf("notification = %q", got)
	}
	buf.Reset()
	ti.brand = "kitty"
	ti.announce("janetcode", "done")
	if got := buf.String(); !strings.HasPrefix(got, "\x1b]99;") {
		t.Fatalf("kitty notification = %q", got)
	}
	buf.Reset()
	ti.notify = false
	ti.announce("janetcode", "done")
	if buf.Len() != 0 {
		t.Fatalf("notifications were turned off: %q", buf.String())
	}
	// A failed turn closes the zone with a non-zero code.
	buf.Reset()
	ti.turnEnded(false, "error")
	if !strings.Contains(buf.String(), "133;D;1") {
		t.Fatalf("failed turn = %q", buf.String())
	}
	env := map[string]string{"JANETCODE_NOTIFY": "off"}
	if notifyEnabled(func(k string) string { return env[k] }) {
		t.Fatal("JANETCODE_NOTIFY=off should silence notifications")
	}
	env["JANETCODE_NOTIFY"] = ""
	env["KITTY_WINDOW_ID"] = "3"
	if !notifyEnabled(func(k string) string { return env[k] }) || termBrand(func(k string) string { return env[k] }) != "kitty" {
		t.Fatal("kitty should be detected and notifications on by default")
	}
}

// TestFocusTracksTerminal: DEC 1004 reports decide whether a turn's end
// is announced at all.
func TestFocusTracksTerminal(t *testing.T) {
	h := newHarness(t)
	if !h.m.term.focused {
		t.Fatal("a terminal that never reports focus counts as focused")
	}
	h.update(tea.BlurMsg{})
	if h.m.term.focused {
		t.Fatal("blur should clear focus")
	}
	h.update(tea.FocusMsg{})
	if !h.m.term.focused {
		t.Fatal("focus should restore it")
	}
	if title := h.m.windowTitle(); !strings.HasPrefix(title, "janetcode · ") {
		t.Fatalf("window title = %q", title)
	}
}

// TestEphemeralNoticeRow: paste-chip and outside branch-changed occupy
// the reserved notice row for about three seconds, then leave it blank,
// and do not append a lasting transcript line. A queued-message notice
// still wins the row.
func TestEphemeralNoticeRow(t *testing.T) {
	h := newHarness(t)
	nEntries := len(h.m.transcript.entries)

	body := strings.Repeat("a line of a log file\n", 40)
	h.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(body), Paste: true})
	v := plain(h.m.View())
	if !strings.Contains(v, "pasted 41 lines as a chip") {
		t.Fatalf("paste confirmation not in the notice row:\n%s", v)
	}
	if len(h.m.transcript.entries) != nEntries {
		t.Fatalf("paste confirmation added a transcript line: %+v", h.m.transcript.entries[nEntries:])
	}

	h.m.turn.queued = []string{"do the next thing"}
	h.m.refresh(false)
	v = plain(h.m.View())
	if !strings.Contains(v, "queued 1") {
		t.Fatalf("queue did not take the notice row:\n%s", v)
	}
	if strings.Contains(v, "pasted 41 lines") {
		t.Fatalf("queue should win over the paste notice:\n%s", v)
	}
	h.m.turn.queued = nil
	h.m.input.Reset()

	h.m.notice.at = time.Now().Add(-noticeFor - time.Second)
	h.update(tickMsg(time.Now()))
	if strings.Contains(plain(h.m.View()), "pasted 41 lines") {
		t.Fatalf("expired paste notice still on screen:\n%s", plain(h.m.View()))
	}

	gitDir := filepath.Join(h.m.opts.Cwd, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.m.branch = "main"
	nEntries = len(h.m.transcript.entries)
	h.m.turn.running = false
	h.m.ticks = 9
	h.update(tickMsg(time.Now()))
	v = plain(h.m.View())
	if !strings.Contains(v, "branch changed: main → feature") {
		t.Fatalf("branch change not in the notice row:\n%s", v)
	}
	if len(h.m.transcript.entries) != nEntries {
		t.Fatalf("branch change added a transcript line: %+v", h.m.transcript.entries[nEntries:])
	}
	h.m.notice.at = time.Now().Add(-noticeFor - time.Second)
	h.update(tickMsg(time.Now()))
	if strings.Contains(plain(h.m.View()), "branch changed") {
		t.Fatalf("expired branch notice still on screen:\n%s", plain(h.m.View()))
	}
}

// TestShortWindowScalesTheInterface: caps follow the terminal height, and
// a very short window drops what is optional rather than crowding out
// the transcript.
func TestShortWindowScalesTheInterface(t *testing.T) {
	if outputCap(90) != 30 || outputCap(6) != 4 || outputCap(300) != 40 {
		t.Fatalf("caps = %d %d %d", outputCap(90), outputCap(6), outputCap(300))
	}
	h := newHarness(t)
	h.update(tea.WindowSizeMsg{Width: 90, Height: 40})
	if h.m.compactMode() || h.m.tight() {
		t.Fatal("a 40-row window is neither compact nor tight")
	}
	if !strings.Contains(plain(h.m.View()), "janetcode test") {
		t.Fatal("the welcome banner should be shown in a tall window")
	}
	h.update(tea.WindowSizeMsg{Width: 90, Height: 18})
	if !h.m.compactMode() || h.m.tight() {
		t.Fatal("an 18-row window is compact, not tight")
	}
	if h.m.menuRows() != 4 {
		t.Fatalf("compact menu rows = %d", h.m.menuRows())
	}
	h.update(tea.WindowSizeMsg{Width: 90, Height: 14})
	if !h.m.tight() {
		t.Fatal("a 14-row window is tight")
	}
	if strings.Contains(plain(h.m.View()), "janetcode test ·") {
		t.Fatalf("the welcome banner survived a tight window:\n%s", plain(h.m.View()))
	}
	// Nothing is persisted: growing the window puts it back.
	h.update(tea.WindowSizeMsg{Width: 90, Height: 40})
	if h.m.tight() || h.m.compactMode() || !strings.Contains(plain(h.m.View()), "janetcode test") {
		t.Fatal("the compact choice outlived the window that caused it")
	}
}

// TestPromptDoesNotJump: the row above the input is reserved, so a
// notice arriving or leaving never moves the input box. A permission
// prompt (warning included) uses that same reserved footer height, so
// it doesn't move the box either. The composer growing with what's
// typed does move the box — by exactly the number of rows it grew, with
// the transcript staying pinned and not otherwise disturbed.
func TestPromptDoesNotJump(t *testing.T) {
	block := make(chan struct{})
	rs := make([]*llm.Response, 5)
	for i := range rs {
		rs[i] = &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "ok"), StopReason: llm.StopEndTurn}
	}
	h := newHarness(t, rs...)
	h.m.opts.Agent.Provider = &heldOpen{until: block, inner: h.m.opts.Agent.Provider}
	promptRow := func() int {
		for i, l := range strings.Split(plain(h.m.View()), "\n") {
			if strings.Contains(l, "❯ ") && strings.Contains(l, "│") {
				return i
			}
		}
		return -1
	}
	h.keys("go")
	h.enter()
	before := promptRow()
	if before < 0 {
		t.Fatalf("no input box:\n%s", plain(h.m.View()))
	}
	for i := 0; i < 3; i++ {
		h.keys(fmt.Sprintf("queued %d", i))
		h.enter()
	}
	if got := promptRow(); got != before {
		t.Fatalf("three queued messages moved the prompt from %d to %d", before, got)
	}
	if v := plain(h.m.View()); !strings.Contains(v, "queued 3") {
		t.Fatalf("the queue is not reported:\n%s", v)
	}
	close(block)
	h.pump(func() bool { return !h.m.turn.running })

	// Reserved input height: a permission prompt (warning included) and
	// the prompt closing must not move the ❯ row or the transcript
	// viewport. Typing inside the cap now grows the box a line at a
	// time (see composerLines), so that case is checked differently
	// below: the prompt row and the viewport must move by exactly the
	// number of lines the box grew, not by some other amount, and once
	// the box shrinks back the frame must return to this baseline.
	promptRowAndView := func() (int, int) {
		h.m.layout()
		h.m.refresh(false)
		return promptRow(), h.m.viewport.Height
	}
	row, vh := promptRowAndView()
	if row < 0 {
		t.Fatalf("no input box after the turn:\n%s", plain(h.m.View()))
	}
	beforeHeight := h.m.inputHeight()
	h.keys("one")
	h.update(tea.KeyMsg{Type: tea.KeyCtrlJ})
	h.keys("two")
	h.update(tea.KeyMsg{Type: tea.KeyCtrlJ})
	h.keys("three")
	grew := h.m.inputHeight() - beforeHeight
	if grew != 2 {
		t.Fatalf("a three-line prompt reported height %d, grown by %d, want +2", h.m.inputHeight(), grew)
	}
	if !h.m.pinned {
		t.Fatalf("the transcript unpinned itself while the composer grew")
	}
	if got, hgt := promptRowAndView(); got != row-grew || hgt != vh-grew {
		t.Fatalf("a %d-line growth moved the prompt from %d/%d to %d/%d, want %d/%d", grew, row, vh, got, hgt, row-grew, vh-grew)
	}
	h.m.input.Reset()
	h.m.pending = &permissionMsg{call: agent.ToolCall{ID: "p1", Name: "write_file", Input: json.RawMessage(`{"path":"n.txt","content":"x"}`)}, reply: make(chan verdict, 1)}
	if got, hgt := promptRowAndView(); got != row || hgt != vh {
		t.Fatalf("a permission prompt moved the prompt from %d/%d to %d/%d", row, vh, got, hgt)
	}
	if v := plain(h.m.View()); !strings.Contains(v, "allow write_file") {
		t.Fatalf("permission notice missing:\n%s", v)
	}
	h.m.pending.call.Warning = "recursive force delete"
	if got, hgt := promptRowAndView(); got != row || hgt != vh {
		t.Fatalf("a permission warning moved the prompt from %d/%d to %d/%d", row, vh, got, hgt)
	}
	if v := plain(h.m.View()); !strings.Contains(v, "⚠ recursive force delete") {
		t.Fatalf("warning not on the reserved row:\n%s", v)
	}
	h.m.pending = nil
	if got, hgt := promptRowAndView(); got != row || hgt != vh {
		t.Fatalf("closing the permission prompt moved the prompt from %d/%d to %d/%d", row, vh, got, hgt)
	}
}

// TestColourFallbacks: NO_COLOR renders every style plain, and a
// sixteen-colour terminal gets ANSI slots instead of truecolor hex
// quantised down to black.
func TestColourFallbacks(t *testing.T) {
	profile := lipgloss.ColorProfile()
	defer lipgloss.SetColorProfile(profile)
	def := theme.Builtins()[0]

	// Truecolor: the palette is hex and the styles carry SGR colours.
	lipgloss.SetColorProfile(termenv.TrueColor)
	st := newStyles(def, false)
	if !strings.Contains(st.err.Render("x"), "\x1b[38;2;") {
		t.Fatalf("truecolor error style = %q", st.err.Render("x"))
	}

	// Sixteen colours: ANSI slots, never a dark hex quantised to black.
	lipgloss.SetColorProfile(termenv.ANSI)
	got, ansi := applyColorProfile(func(string) string { return "" })
	if got != termenv.ANSI || !ansi {
		t.Fatalf("profile = %v, ansi-only = %v", got, ansi)
	}
	st = newStyles(def, ansi)
	if st.col.err.Dark != "9" || st.col.accent.Dark != "12" {
		t.Fatalf("palette not switched: %+v %+v", st.col.err, st.col.accent)
	}
	out := st.err.Render("x")
	if !strings.Contains(out, "\x1b[9") && !strings.Contains(out, "\x1b[3") {
		t.Fatalf("ansi error style = %q", out)
	}
	if strings.Contains(out, "38;2;") {
		t.Fatalf("truecolor survived on a 16-colour terminal: %q", out)
	}

	// NO_COLOR: nothing at all.
	got, ansi = applyColorProfile(func(k string) string {
		if k == "NO_COLOR" {
			return "1"
		}
		return ""
	})
	if got != termenv.Ascii || ansi {
		t.Fatalf("NO_COLOR profile = %v, ansi-only = %v", got, ansi)
	}
	st = newStyles(def, ansi)
	for _, s := range []lipgloss.Style{st.err, st.accent, st.warn, st.key, st.header} {
		if got := s.Render("x"); strings.Contains(got, "\x1b[3") || strings.Contains(got, "\x1b[4") {
			t.Fatalf("NO_COLOR left colour behind: %q", got)
		}
	}
}

// A short terminal used to lose the top of a modal: the dialog always drew
// twelve list rows, and lipgloss.Place centred a box taller than the body,
// so the title, the search field and the highlighted row scrolled off the
// screen. The list now shrinks to what the body can hold.
func TestDialogFitsShortTerminal(t *testing.T) {
	h := newHarness(t)
	cfg := &config.Config{Provider: "fake", Model: "m", Providers: map[string]config.Provider{
		"fake": {Type: "openai", BaseURL: "http://fake", APIKey: "k0"},
	}}
	h.withConfig(t, cfg, func(c *config.Config) (llm.Provider, error) { return &listing{}, nil })
	h.update(tea.WindowSizeMsg{Width: 60, Height: 18})
	h.keys("/connect")
	h.enter()
	if h.m.connect == nil {
		t.Fatal("dialog not open")
	}
	v := plain(h.m.View())
	if lines := strings.Count(v, "\n") + 1; lines > 18 {
		t.Fatalf("view is %d lines at height 18:\n%s", lines, v)
	}
	if !strings.Contains(v, "Connect to a provider") || !strings.Contains(v, "search providers") {
		t.Fatalf("title or search field cut off:\n%s", v)
	}
	if !strings.Contains(v, "▸ ") {
		t.Fatalf("highlight not visible:\n%s", v)
	}
	// Tall terminals keep the full twelve rows.
	h.update(tea.WindowSizeMsg{Width: 100, Height: 40})
	if got := strings.Count(plain(h.m.View()), "\n"); got < 20 {
		t.Fatalf("tall view collapsed to %d lines", got)
	}
}

func TestFitRows(t *testing.T) {
	for _, c := range []struct{ height, chrome, want int }{
		{40, 9, dialogRows}, {18, 9, 9}, {12, 9, 3}, {10, 9, 1}, {4, 9, 0},
	} {
		if got := fitRows(c.height, c.chrome); got != c.want {
			t.Errorf("fitRows(%d,%d) = %d, want %d", c.height, c.chrome, got, c.want)
		}
	}
}

// Notices and verdicts are composed by the agent, which only knows
// absolute paths; one file name could wrap the line twice and bury the
// sentence it belonged to.
func TestNoticePathsAreShortened(t *testing.T) {
	h := newHarness(t)
	cwd := h.m.opts.Cwd
	h.m.transcript.apply(agent.Event{Kind: agent.EvVerdict,
		Text: "⚠ self-checked: 1 file(s) edited and tests passed, but " + filepath.Join(cwd, "main_test.go") + " changed in the same turn"})
	line := h.m.transcript.entries[len(h.m.transcript.entries)-1].text
	if strings.Contains(line, cwd) {
		t.Fatalf("absolute path in the verdict: %s", line)
	}
	if !strings.Contains(line, "main_test.go changed in the same turn") {
		t.Fatalf("verdict: %s", line)
	}
}

// The prompt box grew a line at a time, but the textarea was resized
// after it had already scrolled: it decided where to scroll with the old
// height, and nothing ever scrolled it back. A two-line prompt showed
// only its second line, and the first line of a paste was invisible.
func TestInputBoxShowsItsFirstLine(t *testing.T) {
	h := newHarness(t)
	h.update(tea.WindowSizeMsg{Width: 110, Height: 32})
	paste := func(text string) {
		h.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(text), Paste: true})
	}
	newline := func() { h.update(tea.KeyMsg{Type: tea.KeyCtrlJ}) }
	h.keys("alpha")
	newline()
	h.keys("beta")
	newline()
	h.keys("gamma")
	v := plain(h.m.View())
	for _, want := range []string{"❯ alpha", "beta", "gamma"} {
		if !strings.Contains(v, want) {
			t.Fatalf("three-line prompt lost %q:\n%s", want, v)
		}
	}
	h.m.input.Reset()
	paste("alpha\nbeta\ngamma")
	v = plain(h.m.View())
	if !strings.Contains(v, "❯ alpha") {
		t.Fatalf("first line of a paste not shown:\n%s", v)
	}
	// The invariant behind it: the textarea is never resized after it has
	// already decided where to scroll, because its height never changes.
	if got, want := h.m.input.Height(), h.m.inputLimit(); got != want {
		t.Fatalf("textarea height %d, want the box limit %d", got, want)
	}
	// A paste longer than the box keeps the tail, where the cursor is.
	h.m.input.Reset()
	var lines []string
	for i := 1; i <= 8; i++ {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	paste(strings.Join(lines, "\n"))
	v = plain(h.m.View())
	if !strings.Contains(v, "line 8") {
		t.Fatalf("tail of a long prompt not shown:\n%s", v)
	}
	if strings.Contains(v, "line 1\n") {
		t.Fatalf("box did not scroll for a long prompt:\n%s", v)
	}
	// The box never grows past its limit.
	if got := h.m.inputHeight(); got != maxInputLines {
		t.Fatalf("input height = %d", got)
	}
}

// The prompt's placeholder was one long sentence cut wherever the window
// ended, so a narrow terminal offered "· / for" where it meant "· / for
// commands".
func TestPlaceholderFitsTheWindow(t *testing.T) {
	h := newHarness(t)
	for _, w := range []int{120, 100, 80, 60, 48, 30} {
		h.update(tea.WindowSizeMsg{Width: w, Height: 24})
		p := h.m.placeholder()
		if got := lipgloss.Width(p) + 2; got > w-4 {
			t.Errorf("width %d: placeholder %q takes %d columns", w, p, got)
		}
		if strings.HasSuffix(p, "…") {
			t.Errorf("width %d: placeholder cut mid-phrase: %q", w, p)
		}
	}
	h.update(tea.WindowSizeMsg{Width: 120, Height: 24})
	if h.m.placeholder() != defaultPlaceholder {
		t.Fatalf("a wide window lost the full placeholder: %q", h.m.placeholder())
	}
}

func TestFitPhrase(t *testing.T) {
	if got := fitPhrase(20, "a long phrase that will not fit at all", "short"); got != "short" {
		t.Fatalf("got %q", got)
	}
	if got := fitPhrase(40, "fits fine"); got != "fits fine" {
		t.Fatalf("got %q", got)
	}
	// Nothing fits: the shortest is cut, and says so.
	if got := fitPhrase(6, "aaaaaaaaaaaa", "bbbbbbbbbbbb"); !strings.HasSuffix(got, "…") {
		t.Fatalf("got %q", got)
	}
}

// renderCode cut a long line with truncateLine, which marks the cut
// itself, and then added a second ellipsis: every clipped line of /diff,
// /open and /changes ended in "……".
func TestCodeLinesEndInOneEllipsis(t *testing.T) {
	long := "/diff <path> shows the hunks · /diff revert <path>:<n> reverts one · /undo reverts a whole turn"
	out := plain(renderCode(defaultStyles(), long, 40))
	if strings.Contains(out, "……") {
		t.Fatalf("two ellipses: %q", out)
	}
	if !strings.HasSuffix(strings.TrimRight(out, " "), "…") {
		t.Fatalf("no cut mark: %q", out)
	}
}

// gaugeWhole reports whether one of the context gauge's wordings appears
// in the status line uncut.
func gaugeWhole(m *model, line string) bool {
	for _, g := range m.contextGauges() {
		if strings.Contains(plain(line), plain(g)) {
			return true
		}
	}
	return false
}

// The repository map is attached on every turn; saying so under every
// prompt was noise. It stays in the session log and the trajectory.
func TestRoutineNoticeIsNotShown(t *testing.T) {
	h := newHarness(t)
	before := len(h.m.transcript.entries)
	h.m.transcript.apply(agent.Event{Kind: agent.EvNotice, Text: "repository map attached: 3 files, 1 pulled up by the request"})
	if len(h.m.transcript.entries) != before {
		t.Fatalf("a routine notice was added: %q", h.m.transcript.entries[len(h.m.transcript.entries)-1].text)
	}
	h.m.transcript.apply(agent.Event{Kind: agent.EvNotice, Text: "memory recalled: 2 notes"})
	if len(h.m.transcript.entries) != before+1 {
		t.Fatal("an ordinary notice was dropped too")
	}
}
