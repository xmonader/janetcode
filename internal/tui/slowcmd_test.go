package tui

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/hints"
	"github.com/xmonader/janetcode/internal/keymap"
)

// The slow-command hint recorded the tool's name, so every slow command
// was "bash"; it is the command line.
func TestSlowCommandRecordsTheCommandLine(t *testing.T) {
	h := newHarness(t)
	h.m.hint.engine = hints.New(nil, nil)
	call := &agent.ToolCall{ID: "c1", Name: "bash", Input: json.RawMessage(`{"command":"go test ./..."}`)}
	h.m.observeEvent(agent.Event{Kind: agent.EvToolResult, Call: call, Duration: 45 * time.Second})
	if got := h.m.hint.state.SlowCommand; got != "go test ./..." {
		t.Fatalf("SlowCommand = %q", got)
	}
}

// The hint and the status line name the background key the user bound,
// not the default.
func TestBackgroundKeyFollowsTheKeymap(t *testing.T) {
	h := newHarness(t)
	km, err := keymap.Resolve(keymap.Overrides{string(keymap.Background): {"alt+g"}})
	if err != nil {
		t.Fatal(err)
	}
	h.m.keys = km
	h.m.refreshHintState()
	if got := h.m.hint.state.BackgroundKey; got != "alt+g" {
		t.Fatalf("BackgroundKey = %q", got)
	}
}
