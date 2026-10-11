package tui

import (
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/memory"
)

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
