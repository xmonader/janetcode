package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/perm"
)

func TestPermissionPromptAllow(t *testing.T) {
	h := newHarness(t,
		&llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ID: "w1", Name: "write_file", Input: json.RawMessage(`{"path":"n.txt","content":"x\n"}`)}}}, StopReason: llm.StopToolUse},
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "written"), StopReason: llm.StopEndTurn},
	)
	h.keys("make n.txt")
	h.enter()
	h.pump(func() bool { return h.m.pending != nil })
	v := plain(h.m.View())
	if !strings.Contains(v, "allow write_file") || !strings.Contains(v, "+x") || !strings.Contains(v, "approval needed") || !strings.Contains(v, "all write_file this session") {
		t.Fatalf("prompt not shown:\n%s", v)
	}
	// Typing while the prompt is up must not go into the textarea.
	h.keys("q")
	if h.m.input.Value() != "" {
		t.Fatal("keys leaked into input during permission prompt")
	}
	h.keys("y")
	h.pump(func() bool { return !h.m.turn.running })
	v = plain(h.m.View())
	if !strings.Contains(v, "✓ write_file n.txt") || !strings.Contains(v, "written") {
		t.Fatalf("after allow:\n%s", v)
	}
}

func TestPermissionDenyWithReason(t *testing.T) {
	h := newHarness(t,
		&llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ID: "w1", Name: "write_file", Input: json.RawMessage(`{"path":"n.txt","content":"x\n"}`)}}}, StopReason: llm.StopToolUse},
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "understood"), StopReason: llm.StopEndTurn},
	)
	h.keys("make n.txt")
	h.enter()
	h.pump(func() bool { return h.m.pending != nil })
	h.keys("n")
	if !h.m.denying {
		t.Fatal("n should open the reason prompt")
	}
	if v := plain(h.m.View()); !strings.Contains(v, "tell the model why") {
		t.Fatalf("reason prompt missing:\n%s", v)
	}
	h.keys("wrong directory")
	h.enter()
	h.pump(func() bool { return !h.m.turn.running })
	res := h.m.opts.Agent.Messages[2].Blocks[0]
	if !res.IsError || !strings.Contains(res.Text, "The user said: wrong directory") {
		t.Fatalf("tool result = %q", res.Text)
	}
	v := plain(h.m.View())
	if !strings.Contains(v, "The user said: wrong directory") || !strings.Contains(v, "understood") || h.m.input.Value() != "" || h.m.input.Placeholder != defaultPlaceholder {
		t.Fatalf("view:\n%s", v)
	}
	if _, err := os.Stat(filepath.Join(h.m.opts.Cwd, "n.txt")); err == nil {
		t.Fatal("denied write happened")
	}
}

func TestPermissionBoxShowsWarning(t *testing.T) {
	h := newHarness(t)
	h.m.pending = &permissionMsg{call: agent.ToolCall{ID: "b1", Name: "bash", Input: json.RawMessage(`{"command":"rm -rf build"}`), Warning: "recursive force delete"}, reply: make(chan verdict, 1)}
	h.m.layout()
	v := plain(h.m.View())
	for _, want := range []string{"allow bash rm -rf build", "⚠ recursive force delete", "always for bash", "approval needed"} {
		if !strings.Contains(v, want) {
			t.Errorf("missing %q in view:\n%s", want, v)
		}
	}
	h.keys("n")
	if h.m.pending == nil || !h.m.denying {
		t.Fatal("n should open the reason prompt")
	}
	h.update(tea.KeyMsg{Type: tea.KeyEsc})
	if h.m.pending != nil || h.m.denying {
		t.Fatal("esc should deny without a reason")
	}
}

func TestPermissionPromptRuleKeySavesRule(t *testing.T) {
	h := newHarness(t,
		&llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ID: "t1", Name: "write_file", Input: json.RawMessage(`{"path":"docs/a.md","content":"1"}`)}}}, StopReason: llm.StopToolUse},
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "wrote"), StopReason: llm.StopEndTurn},
	)
	var saved []string
	h.m.opts.Agent.SaveRule = func(r perm.Rule) error { saved = append(saved, r.String()); return nil }
	h.m.opts.Perms = &PermOptions{Save: func(kind string, r perm.Rule) error { saved = append(saved, kind+" "+r.String()); return nil }}
	h.keys("write it")
	h.enter()
	h.pump(func() bool { return h.m.pending != nil })
	if v := plain(h.m.View()); !strings.Contains(v, "save write_file(docs/*)") {
		t.Fatalf("prompt view:\n%s", v)
	}
	h.keys("r")
	h.pump(func() bool { return !h.m.turn.running })
	if len(saved) != 1 || saved[0] != "write_file(docs/*)" || len(h.m.opts.Agent.Rules.Allow) != 1 {
		t.Fatalf("saved = %v rules = %+v", saved, h.m.opts.Agent.Rules)
	}
	h.keys("/permissions deny bash(git push*)")
	h.enter()
	if len(saved) != 2 || saved[1] != "deny bash(git push*)" || len(h.m.opts.Agent.Rules.Deny) != 1 {
		t.Fatalf("saved = %v", saved)
	}
	h.keys("/permissions")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "deny   bash(git push*)") || !strings.Contains(v, "allow  write_file(docs/*)") {
		t.Fatalf("view:\n%s", v)
	}
}

func TestPlanModeHidesToolsUntilApproved(t *testing.T) {
	h := newHarness(t,
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "## Plan\n1. write a.txt"), StopReason: llm.StopEndTurn},
		&llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ID: "t1", Name: "write_file", Input: json.RawMessage(`{"path":"a.txt","content":"done"}`)}}}, StopReason: llm.StopToolUse},
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "executed"), StopReason: llm.StopEndTurn},
	)
	h.m.opts.Agent.AutoApprove = true
	h.keys("/plan approve")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "not in plan mode") {
		t.Fatalf("view:\n%s", v)
	}
	h.keys("/plan add a.txt")
	h.enter()
	h.pump(func() bool { return !h.m.turn.running })
	sc := h.m.opts.Agent.Provider.(*scripted)
	if !h.m.opts.Agent.PlanMode || !strings.Contains(plain(h.m.View()), "PLAN ·") {
		t.Fatalf("plan mode not on; view:\n%s", plain(h.m.View()))
	}
	for _, d := range sc.reqs()[0].Tools {
		if d.Name == "write_file" {
			t.Fatal("write_file offered in plan mode")
		}
	}
	h.keys("/plan approve")
	h.enter()
	h.pump(func() bool { return !h.m.turn.running })
	if h.m.opts.Agent.PlanMode {
		t.Fatal("still in plan mode after approve")
	}
	if got, _ := os.ReadFile(filepath.Join(h.m.opts.Cwd, "a.txt")); string(got) != "done" {
		t.Fatalf("a.txt = %q", got)
	}
	last := sc.reqs()[1].Messages
	if !strings.Contains(last[len(last)-1].Text(), "The plan is approved") {
		t.Fatalf("approval message missing: %q", last[len(last)-1].Text())
	}
}

// TestPermissionKeysFitOneLine: the keys line never exceeds the width,
// whatever the rule's length.
func TestPermissionKeysFitOneLine(t *testing.T) {
	h := newHarness(t)
	for _, w := range []int{60, 80, 110, 200} {
		h.m.width = w
		for _, expandable := range []bool{false, true} {
			line := plain(h.m.permissionKeys("edit_file", "edit_file(/tmp/claude-1000/-home-driver-wspace-janetcode/dd12e125-65a6-415a-b19b-31de5e8c912b/scratchpad/tuiproj/*)", "", expandable))
			if lipgloss.Width(line) > w-6 || !strings.Contains(line, "n") {
				t.Fatalf("width %d expandable=%v: %d cols: %q", w, expandable, lipgloss.Width(line), line)
			}
		}
	}
}

// TestPermissionKeysKeepTheRule: at 80 and 60 columns the line still
// names the rule the r key would save. It used to degrade to a bare
// "r rule", because the rule was cut before the descriptions of the keys
// that do not need one.
func TestPermissionKeysKeepTheRule(t *testing.T) {
	h := newHarness(t)
	for _, w := range []int{60, 80} {
		h.m.width = w
		for _, rule := range []string{"edit_file(cmd/*)", "bash(go test ./internal/...)", "edit_file(internal/tui/*)"} {
			line := plain(h.m.permissionKeys("edit_file", rule, "", false))
			if lipgloss.Width(line) > w-6 {
				t.Fatalf("width %d rule %q: %d cols: %q", w, rule, lipgloss.Width(line), line)
			}
			i := strings.Index(line, " save ")
			if i < 0 {
				t.Fatalf("width %d rule %q: no rule on the line: %q", w, rule, line)
			}
			rest := line[i+len(" save "):]
			if named, _, _ := strings.Cut(rest, " ·"); len(named) < 6 {
				t.Fatalf("width %d rule %q: rule shown as %q", w, rule, named)
			}
		}
	}
}

// TestPermissionKeysShowTheLearnedNoteWhenWide: a wide terminal names why
// r is worth pressing, next to the rule it would save.
func TestPermissionKeysShowTheLearnedNoteWhenWide(t *testing.T) {
	h := newHarness(t)
	h.m.width = 200
	line := plain(h.m.permissionKeys("bash", "bash(go test *)", "approved 4× before, never refused", false))
	if !strings.Contains(line, "bash(go test *)") || !strings.Contains(line, "approved 4×") {
		t.Fatalf("a wide line dropped the learned note:\n%s", line)
	}
}

// TestPermissionKeysDropTheLearnedNoteWhenNarrow: the note is the first
// thing to go once the line no longer fits, before the rule is touched.
func TestPermissionKeysDropTheLearnedNoteWhenNarrow(t *testing.T) {
	h := newHarness(t)
	h.m.width = 60
	line := plain(h.m.permissionKeys("bash", "bash(go test ./internal/...)", "approved 4× before, never refused", false))
	if lipgloss.Width(line) > 60-6 {
		t.Fatalf("narrow line overflowed: %d cols: %q", lipgloss.Width(line), line)
	}
	if strings.Contains(line, "approved") {
		t.Fatalf("a narrow line kept the learned note instead of dropping it first:\n%s", line)
	}
	if !strings.Contains(line, "bash(go") {
		t.Fatalf("the rule itself was dropped before the learned note:\n%s", line)
	}
}

// TestPermissionImpactReservesOneRow: footerHeight claims one extra row
// only while the pending call has something to say about its reach, so a
// call with no impact costs the layout nothing.
func TestPermissionImpactReservesOneRow(t *testing.T) {
	h := newHarness(t)
	h.m.pending = &permissionMsg{call: agent.ToolCall{ID: "e1", Name: "edit_file"}, reply: make(chan verdict, 1)}
	without := h.m.footerHeight()
	h.m.pending.call.Impact = "changes Foo (func) · used 3 times in 2 files"
	with := h.m.footerHeight()
	if with != without+1 {
		t.Fatalf("footerHeight with impact = %d, without = %d; want exactly one more row", with, without)
	}
}

// TestPermissionViewShowsImpact: the footer a user sees during a prompt
// carries the notice row, the impact line and the keys (the input box's
// placeholder).
func TestPermissionViewShowsImpact(t *testing.T) {
	h := newHarness(t)
	h.m.pending = &permissionMsg{call: agent.ToolCall{ID: "e1", Name: "edit_file", Input: json.RawMessage(`{"path":"a.go"}`),
		Impact: "changes Parse (func) · used 3 times in 2 files · tests: parse_test.go"}, reply: make(chan verdict, 1)}
	h.m.layout()
	v := plain(h.m.footerView())
	if !strings.Contains(v, "changes Parse (func) · used 3 times in 2 files · tests: parse_test.go") {
		t.Fatalf("permission view missing the impact line:\n%s", v)
	}
	if !strings.Contains(v, "allow edit_file") {
		t.Fatalf("permission view missing the notice row:\n%s", v)
	}
	if !strings.Contains(v, "deny") {
		t.Fatalf("permission view missing the keys (the input placeholder):\n%s", v)
	}
}

// TestPermissionImpactAbsentWhenEmpty: a call with no impact shows no
// extra row at all — nothing to fit, nothing to reserve.
func TestPermissionImpactAbsentWhenEmpty(t *testing.T) {
	h := newHarness(t)
	h.m.pending = &permissionMsg{call: agent.ToolCall{ID: "e1", Name: "edit_file", Input: json.RawMessage(`{"path":"a.go"}`)}, reply: make(chan verdict, 1)}
	if got := h.m.permissionImpact(); got != "" {
		t.Fatalf("expected no impact row, got %q", got)
	}
}

// TestPermissionImpactNeverWraps: at a narrow (80) and a wide (160)
// terminal, a long impact line is fitted to the width and truncated
// rather than wrapped onto a second line.
func TestPermissionImpactNeverWraps(t *testing.T) {
	h := newHarness(t)
	long := "changes Parse (func), Run (method), VeryLongSymbolNameThatKeepsGoingAndGoing (func) · used 214 times in 60 files · tests: parse_test.go, cli_test.go, another_test.go, yet_another_test.go · checks: go test ./..., go vet ./..., gofmt -l ."
	for _, w := range []int{80, 160} {
		h.m.width = w
		h.m.pending = &permissionMsg{call: agent.ToolCall{ID: "e1", Name: "edit_file", Impact: long}, reply: make(chan verdict, 1)}
		line := plain(h.m.permissionImpact())
		if strings.Contains(line, "\n") {
			t.Fatalf("width %d: impact line wrapped:\n%s", w, line)
		}
		room := w - 4
		if lipgloss.Width(line) > room {
			t.Fatalf("width %d: impact line is %d cols, want <= %d: %q", w, lipgloss.Width(line), room, line)
		}
	}
}

// TestPermissionBoxExpandHint covers item 13: the box offers ctrl+o only
// when there is more of the call to see, and wears the same warning
// colour as the "approval needed" status.
func TestPermissionBoxExpandHint(t *testing.T) {
	short := agent.ToolCall{ID: "e1", Name: "edit_file", Input: json.RawMessage(`{"path":"a.go"}`),
		Preview: "--- a.go\n+++ a.go\n@@ -1,1 +1,1 @@\n-old\n+new\n"}
	if pendingExpandable(short) {
		t.Error("a five-line diff has nothing to expand")
	}
	long := short
	for i := 0; i < 200; i++ {
		long.Preview += fmt.Sprintf("+line %d\n", i)
	}
	if !pendingExpandable(long) {
		t.Error("a 200-line diff should offer ctrl+o")
	}
	// A bash command is shown whole on the tool line: never expandable.
	if pendingExpandable(agent.ToolCall{Name: "bash", Input: json.RawMessage(`{"command":"ls"}`)}) {
		t.Error("bash offered ctrl+o")
	}

	h := newHarness(t,
		&llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ID: "w1", Name: "write_file", Input: json.RawMessage(`{"path":"n.txt","content":"x\n"}`)}}}, StopReason: llm.StopToolUse},
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "ok"), StopReason: llm.StopEndTurn},
	)
	h.keys("make n.txt")
	h.enter()
	h.pump(func() bool { return h.m.pending != nil })
	v := plain(h.m.footerView())
	if !strings.Contains(v, "allow write_file") {
		t.Fatalf("permission box:\n%s", v)
	}
	if strings.Contains(v, "ctrl+o") {
		t.Errorf("a one-line write offered ctrl+o:\n%s", v)
	}
	// The box border and the status word are the same warning colour.
	if h.m.styles.permBox.GetBorderTopForeground() != h.m.styles.warn.GetForeground() {
		t.Errorf("permission box border %v, warning text %v",
			h.m.styles.permBox.GetBorderTopForeground(), h.m.styles.warn.GetForeground())
	}
	h.keys("y")
	h.pump(func() bool { return !h.m.turn.running })
}

// The prompt offers r to save a rule, and used to print that rule as an
// absolute path truncated in the middle — "edit_file(/tmp/claude-1000/-h…"
// said nothing about what would be allowed. Paths are now written the way
// the rest of the interface writes them.
func TestSuggestedRuleUsesShortPaths(t *testing.T) {
	h := newHarness(t)
	cwd := h.m.opts.Cwd
	got := h.m.suggestedRule("edit_file(" + cwd + "/*)")
	if got != "edit_file(./*)" {
		t.Fatalf("rule in the working directory = %q", got)
	}
	got = h.m.suggestedRule("edit_file(" + filepath.Join(cwd, "internal", "tui") + "/*)")
	if got != "edit_file(internal/tui/*)" {
		t.Fatalf("rule under the working directory = %q", got)
	}
	// A bash rule is a command, not a path, and is left alone.
	if got := h.m.suggestedRule("bash(go test *)"); got != "bash(go test *)" {
		t.Fatalf("bash rule = %q", got)
	}
	// No rule offered: no r key either.
	if line := plain(h.m.permissionKeys("bash", "", "", false)); strings.Contains(line, "rule") || strings.Contains(line, "   r ") {
		t.Fatalf("keys offer a rule when there is none: %q", line)
	}
}

// The banner used to say "esc cancels" about the permission prompt while
// /help said esc denies and c cancels the turn.
func TestBannerMatchesPermissionKeys(t *testing.T) {
	h := newHarness(t)
	v := plain(h.m.View())
	// In ask mode esc denies the one call at a prompt; a banner saying
	// it cancels would teach the wrong key. The keys are on the prompt.
	if !strings.Contains(v, "edits and commands ask first") || strings.Contains(v, "esc cancels") {
		t.Fatalf("banner:\n%s", v)
	}
}

// TestPermissionPromptLadderMovesAndSaves: ← and → move along the rules
// the prompt offers, and r saves the one that is highlighted, not the
// one the prompt opened on.
func TestPermissionPromptLadderMovesAndSaves(t *testing.T) {
	save := func(t *testing.T, move []tea.KeyMsg) []string {
		t.Helper()
		h := newHarness(t,
			&llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ID: "t1", Name: "write_file", Input: json.RawMessage(`{"path":"docs/a.md","content":"1"}`)}}}, StopReason: llm.StopToolUse},
			&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "wrote"), StopReason: llm.StopEndTurn},
		)
		var saved []string
		h.m.opts.Agent.SaveRule = func(r perm.Rule) error { saved = append(saved, r.String()); return nil }
		h.keys("write it")
		h.enter()
		h.pump(func() bool { return h.m.pending != nil })
		// The prompt opens on what r has always saved, and says where
		// that stands on the ladder.
		if v := plain(h.m.View()); !strings.Contains(v, "write_file(docs/*)") || !strings.Contains(v, "2/3") {
			t.Fatalf("prompt view:\n%s", v)
		}
		for _, k := range move {
			h.update(k)
		}
		h.keys("r")
		h.pump(func() bool { return !h.m.turn.running })
		return saved
	}
	left := tea.KeyMsg{Type: tea.KeyLeft}
	right := tea.KeyMsg{Type: tea.KeyRight}
	for _, c := range []struct {
		name string
		move []tea.KeyMsg
		want string
	}{
		{"default", nil, "write_file(docs/*)"},
		{"narrowed", []tea.KeyMsg{left}, "write_file(docs/a.md)"},
		{"back again", []tea.KeyMsg{left, right}, "write_file(docs/*)"},
		{"clamped at the narrow end", []tea.KeyMsg{left, left, left}, "write_file(docs/a.md)"},
		{"the typed rung beside it", []tea.KeyMsg{right}, "write_file(write:docs/*)"},
		{"clamped at the wide end", []tea.KeyMsg{right, right, right}, "write_file(write:docs/*)"},
		{"shift+tab narrows too", []tea.KeyMsg{{Type: tea.KeyShiftTab}}, "write_file(docs/a.md)"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if saved := save(t, c.move); len(saved) != 1 || saved[0] != c.want {
				t.Fatalf("saved = %v, want [%s]", saved, c.want)
			}
		})
	}
}

// TestPermissionKeysShowTheLadderHint: the key line says the rule can be
// moved and where it stands, still fits the widths the prompt has to fit,
// and says nothing at all when there is only one rule to save.
func TestPermissionKeysShowTheLadderHint(t *testing.T) {
	h := newHarness(t)
	for _, w := range []int{60, 80, 160} {
		h.m.width = w
		line := plain(h.m.permissionKeysAt("bash", "bash(go test *)", "", false, 1, 3))
		if lipgloss.Width(line) > w-6 {
			t.Fatalf("width %d: %d cols: %q", w, lipgloss.Width(line), line)
		}
		if w >= 80 && (!strings.Contains(line, "←→") || !strings.Contains(line, "2/3")) {
			t.Fatalf("width %d dropped the ladder hint: %q", w, line)
		}
		if one := plain(h.m.permissionKeysAt("bash", "bash(go test *)", "", false, 0, 1)); strings.Contains(one, "←→") || strings.Contains(one, "1/1") {
			t.Fatalf("width %d: a single rung got ladder chrome: %q", w, one)
		}
	}
}

// The frame is exactly the terminal's height while a prompt with an
// impact line is up, and after it closes. footerHeight grew by the
// impact row but the viewport was sized only at the next resize, so the
// frame was one row too tall and the terminal scrolled the header away.
func TestImpactPromptKeepsTheHeaderOnScreen(t *testing.T) {
	h := newHarness(t)
	h.update(tea.WindowSizeMsg{Width: 80, Height: 24})
	rows := func() int { return strings.Count(h.m.View(), "\n") + 1 }
	if rows() != 24 {
		t.Fatalf("idle frame has %d rows, want 24", rows())
	}
	h.update(permissionMsg{call: agent.ToolCall{ID: "e1", Name: "edit_file", Input: json.RawMessage(`{"path":"a.go"}`),
		Impact: "changes Parse (func) · used 3 times in 2 files"}, reply: make(chan verdict, 1)})
	if rows() != 24 {
		t.Fatalf("frame with an impact prompt has %d rows, want 24:\n%s", rows(), plain(h.m.View()))
	}
	h.m.closePending()
	if rows() != 24 {
		t.Fatalf("frame after the prompt closed has %d rows, want 24", rows())
	}
}

// After n the prompt collects a reason. The status line kept saying
// "approval needed" and the keys were printed twice (notice row and
// placeholder), so y and a looked like answers and were typed instead.
func TestDenyingSaysSoOnce(t *testing.T) {
	h := newHarness(t)
	h.update(tea.WindowSizeMsg{Width: 120, Height: 30})
	h.update(permissionMsg{call: agent.ToolCall{ID: "e1", Name: "edit_file", Input: json.RawMessage(`{"path":"a.go"}`)}, reply: make(chan verdict, 1)})
	h.keys("n")
	v := plain(h.m.View())
	if strings.Contains(v, "approval needed") || !strings.Contains(v, "denied · waiting for your reason") {
		t.Fatalf("status does not say the call was denied:\n%s", v)
	}
	if n := strings.Count(v, "enter sends"); n != 1 {
		t.Fatalf("the keys are shown %d times, want once:\n%s", n, v)
	}
}

// TestTwoPromptsAtOnceAreBothAnswered: two subagents running side by side
// (the task tool is not mutating, so a batch of them runs in parallel)
// can each bubble a prompt up at the same moment. The second replaced the
// first, whose caller then waited for an answer nobody could give, and
// the batch — and so the turn — never finished.
func TestTwoPromptsAtOnceAreBothAnswered(t *testing.T) {
	h := newHarness(t)
	first, second := make(chan verdict, 1), make(chan verdict, 1)
	h.update(permissionMsg{ctx: context.Background(), call: agent.ToolCall{ID: "c1", Name: "write_file"}, reply: first})
	h.update(permissionMsg{ctx: context.Background(), call: agent.ToolCall{ID: "c2", Name: "write_file"}, reply: second})
	h.keys("y")
	select {
	case v := <-first:
		if v.d != agent.Allow {
			t.Fatalf("first answered %v", v.d)
		}
	default:
		t.Fatal("the first prompt was never answered: the second replaced it")
	}
	if h.m.pending == nil || h.m.pending.call.ID != "c2" {
		t.Fatalf("the second prompt is not up after the first was answered: %+v", h.m.pending)
	}
	h.keys("n")
	h.enter()
	if v := <-second; v.d != agent.Deny {
		t.Fatalf("second answered %v", v.d)
	}
	if h.m.pending != nil {
		t.Fatal("a prompt is still up with nothing left to ask")
	}
}

// TestAPromptWhoseCallerHasGoneIsNotShown: a prompt that waited behind
// another is dropped, not put up, when the caller stopped waiting for it
// in the meantime; and a turn ending does not take down a prompt some
// other caller is still waiting on.
func TestAPromptWhoseCallerHasGoneIsNotShown(t *testing.T) {
	h := newHarness(t)
	gone, cancel := context.WithCancel(context.Background())
	h.update(permissionMsg{ctx: context.Background(), call: agent.ToolCall{ID: "c1", Name: "write_file"}, reply: make(chan verdict, 1)})
	h.update(permissionMsg{ctx: gone, call: agent.ToolCall{ID: "c2", Name: "write_file"}, reply: make(chan verdict, 1)})
	cancel()
	h.keys("y")
	if h.m.pending != nil {
		t.Fatalf("a prompt nobody is waiting on was put up: %+v", h.m.pending.call)
	}
	live := make(chan verdict, 1)
	h.update(permissionMsg{ctx: context.Background(), call: agent.ToolCall{ID: "c3", Name: "write_file"}, reply: live})
	h.m.finishTurn(agentDoneMsg{})
	if h.m.pending == nil || h.m.pending.call.ID != "c3" {
		t.Fatal("the end of a turn took down a prompt its caller is still waiting on")
	}
}

// TestCancellingAtAPromptDoesNotPutUpTheNext: c at a prompt cancels the
// whole turn. The prompt queued behind it, from the same turn, came up at
// once — with an "approval needed" announcement — because the queue moved
// on before the cancel was sent. A prompt whose caller is not the turn
// still comes up.
func TestCancellingAtAPromptDoesNotPutUpTheNext(t *testing.T) {
	h := newHarness(t)
	turnCtx, cancel := context.WithCancel(context.Background())
	h.m.turn.running = true
	h.m.turn.cancel = cancel
	h.update(permissionMsg{ctx: turnCtx, call: agent.ToolCall{ID: "c1", Name: "write_file"}, reply: make(chan verdict, 1)})
	h.update(permissionMsg{ctx: turnCtx, call: agent.ToolCall{ID: "c2", Name: "write_file"}, reply: make(chan verdict, 1)})
	h.update(permissionMsg{ctx: context.Background(), call: agent.ToolCall{ID: "c3", Name: "write_file"}, reply: make(chan verdict, 1)})
	h.keys("c")
	if h.m.pending == nil || h.m.pending.call.ID != "c3" {
		t.Fatalf("after cancelling, the prompt up is %+v, want the one outside the turn", h.m.pending)
	}
}
