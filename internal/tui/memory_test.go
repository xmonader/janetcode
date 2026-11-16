package tui

import (
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/memory"
)

// /sleep and /dream call the model. With no provider connected (a first
// run that closed /connect, then /resume of an old session) /sleep handed
// a nil provider to the call and the window died on a nil dereference.
func TestSleepWithoutAProviderSaysSo(t *testing.T) {
	h := newHarness(t, &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "ok"), StopReason: llm.StopEndTurn})
	h.m.opts.Memory = memory.Open(t.TempDir(), h.m.opts.Cwd)
	h.keys("remember that the build uses make")
	h.enter()
	h.pump(func() bool { return !h.m.turn.running })
	h.m.opts.Agent.UpdateSettings(func(s *agent.Settings) { s.Provider = nil })
	for _, line := range []string{"/sleep", "/dream"} {
		h.runCmd(h.m.command(line))
		if !transcriptHas(h.m, "/connect") {
			t.Fatalf("%s without a provider:\n%s", line, plain(h.m.View()))
		}
	}
}

func TestMemoryAutoAndWhyCommands(t *testing.T) {
	h := newHarness(t)
	store := memory.Open(t.TempDir(), h.m.opts.Cwd)
	h.m.opts.Memory = store
	h.m.command("/memory auto off project")
	if store.AutoAllowed() || !store.AutoProjectOff() {
		t.Fatal("/memory auto off project did not switch the project off")
	}
	if v := plain(h.m.View()); !strings.Contains(v, "off for this project") {
		t.Fatalf("no confirmation shown:\n%s", v)
	}
	h.m.command("/memory auto on project")
	if !store.AutoAllowed() {
		t.Fatal("/memory auto on project did not switch it back on")
	}
	h.m.command("/memory why")
	if v := plain(h.m.View()); !strings.Contains(v, "no turn has run") {
		t.Fatalf("/memory why before a turn:\n%s", v)
	}
}

// "/memory show" with no name searched the memory for the word "show"
// and answered that nothing matched it.
func TestMemoryVerbAloneIsAUsageError(t *testing.T) {
	h := newHarness(t)
	store := memory.Open(t.TempDir(), h.m.opts.Cwd)
	h.m.opts.Memory = store
	if _, _, err := store.Put(memory.Entry{Body: "the build uses make", Scope: "project", Source: "user"}); err != nil {
		t.Fatal(err)
	}
	for _, verb := range []string{"show", "about", "history"} {
		h.m.command("/memory " + verb)
		if v := plain(h.m.View()); !strings.Contains(v, "usage: /memory "+verb) || strings.Contains(v, "nothing remembered matches") {
			t.Fatalf("/memory %s:\n%s", verb, v)
		}
	}
}
