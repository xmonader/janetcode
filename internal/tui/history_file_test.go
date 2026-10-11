package tui

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

// fullHistory writes a history file that is already at the cap.
func fullHistory(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "history", "x.jsonl")
	old := make([]histEntry, histCap)
	for i := range old {
		old[i] = histEntry{Text: fmt.Sprintf("old %d", i)}
	}
	if err := rewritePromptHistory(path, old); err != nil {
		t.Fatal(err)
	}
	return path
}

func openHistory(path string) *historyState {
	h := &historyState{file: path, entries: loadPromptHistory(path)}
	h.idx = len(h.entries)
	return h
}

func onDisk(path string) map[string]bool {
	got := map[string]bool{}
	for _, e := range loadPromptHistory(path) {
		got[e.Text] = true
	}
	return got
}

// Two sessions in one directory, both holding a full history: each used
// to rewrite the file from its own copy once past the cap, so the second
// to save erased what the first had just added.
func TestTwoSessionsAtTheCapKeepEachOthersPrompts(t *testing.T) {
	path := fullHistory(t)
	a, b := openHistory(path), openHistory(path)
	a.remember("from a")
	b.remember("from b")
	got := loadPromptHistory(path)
	if len(got) != histCap {
		t.Fatalf("file holds %d entries, want the cap %d", len(got), histCap)
	}
	have := onDisk(path)
	if !have["from a"] || !have["from b"] {
		t.Fatalf("a concurrent session's prompt was lost: a=%v b=%v", have["from a"], have["from b"])
	}
	if got[len(got)-1].Text != "from b" || got[len(got)-2].Text != "from a" {
		t.Fatalf("newest entries = %+v", got[len(got)-2:])
	}
	// Each session's own up/down history is still its own.
	if a.entries[len(a.entries)-1].Text != "from a" || len(a.entries) != histCap {
		t.Fatalf("a's entries: %d, last %q", len(a.entries), a.entries[len(a.entries)-1].Text)
	}
}

// Saves racing from two sessions lose nothing either: every prompt both
// sent is in the file afterwards.
func TestConcurrentSessionsSavingLoseNothing(t *testing.T) {
	path := fullHistory(t)
	sessions := []*historyState{openHistory(path), openHistory(path)}
	const each = 200
	var wg sync.WaitGroup
	for s, h := range sessions {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				h.remember(fmt.Sprintf("s%d p%d", s, i))
			}
		}()
	}
	wg.Wait()
	have := onDisk(path)
	for s := range sessions {
		for i := 0; i < each; i++ {
			if p := fmt.Sprintf("s%d p%d", s, i); !have[p] {
				t.Fatalf("lost %q", p)
			}
		}
	}
	if n := len(loadPromptHistory(path)); n != histCap {
		t.Fatalf("file holds %d entries, want %d", n, histCap)
	}
}
