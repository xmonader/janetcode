package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

// The controller is additive up, multiplicative down, floored at low,
// capped at the session's tier (high when it has none), and moves at
// most maxEffortMoves times a turn.
func TestEffortControlIsAIMD(t *testing.T) {
	var c effortControl
	c.start("")
	if c.tier() != "low" {
		t.Fatalf("a turn starts at %q, want low", c.tier())
	}
	if !c.observe(effortTrouble) || c.tier() != "medium" {
		t.Fatalf("trouble from low went to %q, want medium", c.tier())
	}
	c.observe(effortTrouble)
	if c.tier() != "high" {
		t.Fatalf("second trouble went to %q, want high", c.tier())
	}
	if c.observe(effortTrouble) || c.tier() != "high" {
		t.Fatalf("trouble at the default ceiling moved to %q", c.tier())
	}
	if !c.observe(effortProgress) || c.tier() != "medium" {
		t.Fatalf("progress from high went to %q, want medium (half the distance above low)", c.tier())
	}
	if !c.observe(effortProgress) || c.tier() != "low" {
		t.Fatalf("progress from medium went to %q, want low", c.tier())
	}
	if c.observe(effortProgress) || c.observe(effortOverthink) || c.observe(effortSteady) {
		t.Fatal("a signal that leaves the tier where it is counted as a move")
	}

	c.start("max")
	for range 4 {
		c.observe(effortTrouble)
	}
	if c.tier() != "max" {
		t.Fatalf("four troubles under a max ceiling reached %q", c.tier())
	}
	if c.moves != maxEffortMoves || c.observe(effortOverthink) || c.tier() != "max" {
		t.Fatalf("a fifth move was allowed: moves=%d tier=%q", c.moves, c.tier())
	}

	c.start("max")
	c.observe(effortTrouble)
	c.observe(effortTrouble)
	c.observe(effortTrouble)
	if !c.observe(effortOverthink) || c.tier() != "low" {
		t.Fatalf("overthink from xhigh went to %q, want low", c.tier())
	}
}

// The ceiling is the session's tier, followed live, and a session set to
// low keeps every call at low.
func TestEffortControlCeilingIsTheSessionTier(t *testing.T) {
	var c effortControl
	c.start("low")
	if c.observe(effortTrouble) || c.tier() != "low" {
		t.Fatalf("a session set to low was raised to %q", c.tier())
	}
	c.start("max")
	c.observe(effortTrouble)
	c.observe(effortTrouble)
	c.setCeil("medium")
	if c.tier() != "medium" {
		t.Fatalf("an /effort medium mid-turn left the tier at %q", c.tier())
	}
}

func use(name, input string) llm.Block {
	return llm.Block{Type: llm.BlockToolUse, Name: name, Input: json.RawMessage(input)}
}

func res(text string, isErr bool) llm.Block {
	return llm.Block{Type: llm.BlockToolResult, Text: text, IsError: isErr}
}

func TestStepEffortSignal(t *testing.T) {
	edit := use("edit_file", `{"path":"a.go","old_string":"x","new_string":"y"}`)
	check := use("bash", `{"command":"go test ./..."}`)
	cases := []struct {
		name    string
		uses    []llm.Block
		results []llm.Block
		want    effortSignal
	}{
		{"failed edit", []llm.Block{edit}, []llm.Block{res("old_string not found", true)}, effortTrouble},
		{"first edit to a file", []llm.Block{edit}, []llm.Block{res("ok", false)}, effortProgress},
		{"green check", []llm.Block{check}, []llm.Block{res("ok\n[exit code 0]", false)}, effortProgress},
		{"green check without an exit line", []llm.Block{check}, []llm.Block{res("ok", false)}, effortProgress},
		{"red check", []llm.Block{check}, []llm.Block{res("FAIL\n[exit code 1]", false)}, effortTrouble},
		{"new edit then red check", []llm.Block{edit, check}, []llm.Block{res("ok", false), res("FAIL\n[exit code 1]", false)}, effortTrouble},
		{"reads only", []llm.Block{use("read_file", `{"path":"a.go"}`), use("grep", `{"pattern":"x"}`)}, []llm.Block{res("...", false), res("...", false)}, effortSteady},
		{"a command that is not a check fails", []llm.Block{use("bash", `{"command":"ls nope"}`)}, []llm.Block{res("no such file\n[exit code 2]", false)}, effortSteady},
		{"the check command was not found", []llm.Block{use("bash", `{"command":"pytest -q"}`)}, []llm.Block{res("bash: pytest: command not found\n[exit code 127]", false)}, effortSteady},
		{"an edit the user denied", []llm.Block{edit}, []llm.Block{res(DeniedMessage, true)}, effortSteady},
		{"an edit the test guard refused", []llm.Block{use("write_file", `{"path":"test_x.py","content":""}`)}, []llm.Block{res(strings.Replace(testGuardNote, "%s", "test_x.py", 1), true)}, effortSteady},
		{"a loop note on a read", []llm.Block{use("read_file", `{"path":"a.go"}`)}, []llm.Block{res("...\n\n[harness] This is the 3rd identical read_file call this turn. If you already have what you need from it, move on; if not, change the input.", false)}, effortTrouble},
		{"more uses than results", []llm.Block{edit, edit}, []llm.Block{res("ok", false)}, effortProgress},
	}
	for _, tc := range cases {
		if got := stepEffortSignal(tc.uses, tc.results, map[string]bool{}); got != tc.want {
			t.Errorf("%s: %s, want %s", tc.name, got, tc.want)
		}
	}

	// Only the first edit to a file is progress: a second one to the same
	// file is the turn reworking what it already wrote.
	edited := map[string]bool{}
	stepEffortSignal([]llm.Block{edit}, []llm.Block{res("ok", false)}, edited)
	if got := stepEffortSignal([]llm.Block{edit}, []llm.Block{res("ok", false)}, edited); got != effortSteady {
		t.Fatalf("a second edit to the same file read as %s", got)
	}
}

// Every note the loop detector, the repeated-message check and the burst
// cap's repeat cut write is one the controller recognises; a rewording
// there that this misses would quietly stop the controller hearing loops.
func TestLoopNotesAreTrouble(t *testing.T) {
	var notes []string
	add := func(n string) {
		if n != "" {
			notes = append(notes, n)
		}
	}
	var d loopDetector
	d.reset()
	for range loopNudgeAt {
		n, _ := d.record("read_file", json.RawMessage(`{"path":"a.go"}`), false, false)
		add(n)
	}
	d.reset()
	for range loopFailNudgeAt {
		n, _ := d.record("bash", json.RawMessage(`{"command":"make"}`), true, false)
		add(n)
	}
	d.reset()
	for i := range 2 * alternateNudgeAt {
		in := `{"path":"a.go"}`
		if i%2 == 1 {
			in = `{"path":"b.go"}`
		}
		n, _ := d.record("read_file", json.RawMessage(in), false, false)
		add(n)
	}
	d.reset()
	for i := range 2 * cycleNudgeAt {
		if i%2 == 0 {
			n, _ := d.record("edit_file", json.RawMessage(`{"path":"a.go","old_string":"`+strings.Repeat("x", i+1)+`"}`), false, true)
			add(n)
		} else {
			n, _ := d.record("bash", json.RawMessage(`{"command":"go test"}`), true, false)
			add(n)
		}
	}
	d.reset()
	for range sameTextNudgeAt {
		n, _ := d.recordText(strings.Repeat("the same long message, again and again. ", 3))
		add(n)
	}
	add(burstNote(3, true))
	if len(notes) < 5 {
		t.Fatalf("drove only %d notes out of the detectors: %q", len(notes), notes)
	}
	for _, n := range notes {
		if !loopNoteRe.MatchString(n) {
			t.Errorf("loop note not recognised: %q", n)
		}
	}
	if loopNoteRe.MatchString(burstNote(3, false)) {
		t.Error("a plain burst cut (no repeat) read as a loop")
	}
}

func TestReplyEffortSignal(t *testing.T) {
	cut := &llm.Response{StopReason: llm.StopMaxTokens, Message: llm.Message{Role: llm.RoleAssistant}}
	if replyEffortSignal(cut, false) != effortOverthink {
		t.Error("a reply cut at the output limit before acting is not overthinking")
	}
	if replyEffortSignal(text("done"), true) != effortOverthink {
		t.Error("a plan-budget cut is not overthinking")
	}
	acting := toolCall("1", "bash", `{"command":"ls"}`)
	acting.StopReason = llm.StopMaxTokens
	if replyEffortSignal(acting, false) != effortSteady {
		t.Error("a reply cut while calling a tool read as overthinking")
	}
	if replyEffortSignal(text("done"), false) != effortSteady {
		t.Error("an ordinary reply read as overthinking")
	}
}

// tiered is a provider that sends a reasoning tier; neutral says whether
// its cache keys on it.
type tiered struct {
	fake
	neutral bool
}

func (*tiered) SupportsEffort() bool          { return true }
func (p *tiered) EffortOutsideCacheKey() bool { return p.neutral }

// Over a real turn the tier follows the steps: low to start, a rung up
// after a failed write, back down after the first good one. The
// assistant events record what each call asked for.
func TestEffortControlSteersCalls(t *testing.T) {
	p := &tiered{neutral: true, fake: fake{responses: []*llm.Response{
		toolCall("1", "write_file", `{"content":"no path"}`),
		toolCall("2", "write_file", `{"path":"new.txt","content":"x"}`),
		text("done"),
	}}}
	a, _ := newAgent(t, &p.fake, nil)
	a.Provider = p
	a.AutoApprove = true
	a.Effort = "high"
	a.EffortControl = true
	var notices []string
	if err := a.Run(context.Background(), "go", func(e Event) {
		if e.Kind == EvNotice {
			notices = append(notices, e.Text)
		}
	}); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range p.requests {
		got = append(got, r.Effort)
	}
	if strings.Join(got, ",") != "low,medium,low" {
		t.Fatalf("tiers asked for = %v, want low,medium,low", got)
	}
	for _, n := range notices {
		if strings.Contains(n, "effort_control") {
			t.Fatalf("an active controller said it was idle: %q", n)
		}
	}
}

// Where it cannot act without cost — a provider that sends no tier, or
// one whose cache keys on it — the controller leaves every call at the
// session's tier and says so once; switched off it does nothing at all.
func TestEffortControlIdleWhereItCannotAct(t *testing.T) {
	run := func(prov llm.Provider, f *fake, on bool) []string {
		t.Helper()
		f.responses = []*llm.Response{toolCall("1", "write_file", `{"content":"no path"}`), text("done")}
		a, _ := newAgent(t, f, nil)
		a.Provider = prov
		a.AutoApprove = true
		a.Effort = "high"
		a.EffortControl = on
		var notices []string
		if err := a.Run(context.Background(), "go", func(e Event) {
			if e.Kind == EvNotice && strings.Contains(e.Text, "effort_control") {
				notices = append(notices, e.Text)
			}
		}); err != nil {
			t.Fatal(err)
		}
		for _, r := range f.requests {
			if r.Effort != "high" {
				t.Fatalf("an idle controller sent %q, want the session's high", r.Effort)
			}
		}
		return notices
	}
	keyed := &tiered{neutral: false}
	if n := run(keyed, &keyed.fake, true); len(n) != 1 || !strings.Contains(n[0], "cache") {
		t.Fatalf("cache-keyed provider: notices %q, want one about the cache", n)
	}
	plain := &fake{}
	if n := run(plain, plain, true); len(n) != 1 || !strings.Contains(n[0], "does not send") {
		t.Fatalf("tierless provider: notices %q, want one saying so", n)
	}
	neutral := &tiered{neutral: true}
	if n := run(neutral, &neutral.fake, false); len(n) != 0 {
		t.Fatalf("switched off, it still spoke: %q", n)
	}
}
