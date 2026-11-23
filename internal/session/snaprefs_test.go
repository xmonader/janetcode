package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSnapshotRefsReadsRecentUndoRecordsOnly(t *testing.T) {
	dir := t.TempDir()
	recent, stale := strings.Repeat("a", 40), strings.Repeat("b", 40)
	write := func(id, commit string, age time.Duration) {
		t.Helper()
		body := `{"checkpoints":[{"snap":{"commit":"` + commit + `"}}]}`
		if err := os.WriteFile(filepath.Join(dir, id+".undo.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		jsonl := filepath.Join(dir, id+".jsonl")
		if err := os.WriteFile(jsonl, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		when := time.Now().Add(-age)
		if err := os.Chtimes(jsonl, when, when); err != nil {
			t.Fatal(err)
		}
	}
	write("s1", recent, time.Hour)
	write("s2", stale, 60*24*time.Hour)
	got := SnapshotRefs(dir, time.Now().Add(-30*24*time.Hour))
	if !got[recent] || got[stale] || len(got) != 1 {
		t.Fatalf("refs = %v", got)
	}
}
