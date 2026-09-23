package tui

import (
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/llm"
)

// updateGolden rewrites the golden frames: go test ./internal/tui -run
// TestGoldenFrames -update. Read the diff before committing it — that
// diff is the review of what the screen now looks like.
var updateGolden = flag.Bool("update", false, "rewrite the TUI golden frames in testdata/golden")

// The TUI's visual gate: each canonical screen is drawn through the real
// model from agent events (no provider, no clock), and the frame must
// match testdata/golden/<name>.txt character for character. A layout or
// wording change shows up here as a diff of the screen itself.
func TestGoldenFrames(t *testing.T) {
	for _, g := range []struct {
		name          string
		width, height int
		build         func(h *harness)
	}{
		{"welcome", 100, 24, func(h *harness) {}},
		{"turn", 100, 50, goldenTurn},
		{"turn-narrow", 60, 60, goldenTurn},
		{"permission", 100, 30, func(h *harness) {
			h.m.transcript.user("clean the build and rebuild")
			h.m.turn.running = true
			h.update(permissionMsg{call: agent.ToolCall{ID: "c1", Name: "bash",
				Input: json.RawMessage(`{"command":"rm -rf build && make -j8 all"}`)}})
		}},
		{"error", 100, 24, func(h *harness) {
			h.m.transcript.user("say hi")
			h.m.transcript.errorf("%s", llm.Explain(errors.New("Unauthorized (HTTP 401)"), "dashscope", tuiRemedies).String())
		}},
	} {
		t.Run(g.name, func(t *testing.T) {
			opts := fakeOptions(t)
			// A fixed working directory keeps the header the same width
			// on every machine; the temp dir's name is random.
			opts.Cwd = "/home/u/proj"
			h := newHarnessFor(t, opts)
			h.update(tea.WindowSizeMsg{Width: g.width, Height: g.height})
			g.build(h)
			h.m.refresh(false)
			got := stableFrame(plain(h.m.View()))
			path := filepath.Join("testdata", "golden", g.name+".txt")
			if *updateGolden {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("no golden frame %s (go test ./internal/tui -run TestGoldenFrames -update): %v", path, err)
			}
			if got != string(want) {
				t.Errorf("%s changed; if intended, rerun with -update and review the diff.\n--- got\n%s\n--- want\n%s", g.name, got, want)
			}
		})
	}
}

// goldenTurn is a whole turn: the prompt, a thought, a read, a failing
// test run, an edit with its diff, a passing run, the answer and the
// verdict.
func goldenTurn(h *harness) {
	m := h.m
	m.transcript.user("Some tests in this repo are failing. Find out why and fix the code, not the tests.")
	ev := func(e agent.Event) { m.applyEvent(e) }
	call := func(id, name, input string) *agent.ToolCall {
		return &agent.ToolCall{ID: id, Name: name, Input: json.RawMessage(input)}
	}
	ev(agent.Event{Kind: agent.EvThinking, Text: "The holiday tests fail. add_business_days never looks at the holidays set."})
	ev(agent.Event{Kind: agent.EvTextBlockDone})
	read := call("c1", "read_file", `{"path":"/home/u/proj/dates.py"}`)
	ev(agent.Event{Kind: agent.EvToolStart, Call: read})
	ev(agent.Event{Kind: agent.EvToolRun, Call: read})
	ev(agent.Event{Kind: agent.EvToolResult, Call: read, Output: "1\tfrom datetime import date, timedelta\n2\t\n3\tdef add_business_days(start, n, holidays=frozenset()):"})
	red := call("c2", "bash", `{"command":"cd /home/u/proj && python -m pytest -q"}`)
	ev(agent.Event{Kind: agent.EvToolStart, Call: red})
	ev(agent.Event{Kind: agent.EvToolRun, Call: red})
	ev(agent.Event{Kind: agent.EvToolResult, Call: red, Output: "..FFF\nFAILED test_dates.py::test_skips_holiday\n3 failed, 2 passed in 0.01s\n[exit code 1]"})
	edit := call("c3", "edit_file", `{"path":"/home/u/proj/dates.py","old_string":"        if d.weekday() < 5:","new_string":"        if d.weekday() < 5 and d not in holidays:"}`)
	edit.Preview = "--- a/dates.py\n+++ b/dates.py\n@@ -15,1 +15,1 @@\n-        if d.weekday() < 5:\n+        if d.weekday() < 5 and d not in holidays:\n"
	ev(agent.Event{Kind: agent.EvToolStart, Call: edit})
	ev(agent.Event{Kind: agent.EvToolRun, Call: edit})
	ev(agent.Event{Kind: agent.EvToolResult, Call: edit, Output: "Edited dates.py (1 replacement, +1 -1 lines)"})
	green := call("c4", "bash", `{"command":"python -m pytest -q"}`)
	ev(agent.Event{Kind: agent.EvToolStart, Call: green})
	ev(agent.Event{Kind: agent.EvToolRun, Call: green})
	ev(agent.Event{Kind: agent.EvToolResult, Call: green, Output: ".....\n5 passed in 0.01s\n[exit code 0]"})
	ev(agent.Event{Kind: agent.EvText, Text: "Fixed. `add_business_days` counted holidays as working days:\n\n```python\nif d.weekday() < 5 and d not in holidays:\n```\n\nAll **5 tests** pass now."})
	ev(agent.Event{Kind: agent.EvTextBlockDone})
	ev(agent.Event{Kind: agent.EvVerdict, Text: "✓ verified: 1 file edited; " + agent.FlippedWhy})
	ev(agent.Event{Kind: agent.EvTurnDone})
	m.status = "✓ verified · done in 1m42s · in 69k · out 3.2k · 1 file changed · /undo reverts"
}

// stableFrame takes out what changes between two runs of the same
// screen: elapsed times the tool rows measure with a real clock.
var clockRe = regexp.MustCompile(`\b\d+(?:\.\d+)?(?:ms|µs|s)\b|<1ms`)

func stableFrame(s string) string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		out = append(out, strings.TrimRight(clockRe.ReplaceAllStringFunc(l, func(m string) string {
			if strings.HasPrefix(m, "1m") || m == "0.01s" {
				return m
			}
			return "<t>"
		}), " "))
	}
	return strings.Join(out, "\n") + "\n"
}
