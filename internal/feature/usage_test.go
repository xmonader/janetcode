package feature

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCountsRoundTripThroughTheConfigDirectory(t *testing.T) {
	dir := t.TempDir()
	u := OpenUsage(dir)
	if u.Used("/undo") {
		t.Fatal("a fresh install had already used /undo")
	}
	u.Note("/undo")
	u.Note("/undo")
	u.Note("@path")
	if err := u.Flush(); err != nil {
		t.Fatal(err)
	}
	again := OpenUsage(dir)
	if again.Count("/undo") != 2 || !again.Used("@path") || again.Used("/goal") {
		t.Fatalf("read back /undo=%d @path=%v /goal=%v", again.Count("/undo"), again.Used("@path"), again.Used("/goal"))
	}
}

func TestNothingIsWrittenUntilFlush(t *testing.T) {
	dir := t.TempDir()
	u := OpenUsage(dir)
	u.Note("/undo")
	if _, err := os.Stat(filepath.Join(dir, usageFile)); err == nil {
		t.Fatal("the counts were written before exit")
	}
	if err := u.Flush(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, usageFile)); err != nil {
		t.Fatalf("nothing was written at exit: %v", err)
	}
}

func TestFlushWithNothingRecordedWritesNoFile(t *testing.T) {
	dir := t.TempDir()
	u := OpenUsage(dir)
	if err := u.Flush(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, usageFile)); err == nil {
		t.Fatal("a session that used nothing still wrote a file")
	}
}

func TestACorruptedFileIsTreatedAsEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, usageFile), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	u := OpenUsage(dir)
	if u.Used("/undo") {
		t.Fatal("a corrupted file produced counts")
	}
	u.Note("/undo")
	if err := u.Flush(); err != nil {
		t.Fatalf("could not recover from a corrupted file: %v", err)
	}
}

func TestNoUsageStoreIsHarmless(t *testing.T) {
	var u *Usage
	u.Note("/undo")
	if u.Used("/undo") || u.Count("/undo") != 0 || u.Flush() != nil || u.Path() != "" {
		t.Fatal("a nil store misbehaved")
	}
	if got := OpenUsage(""); got.Flush() != nil || got.Path() != "" {
		t.Fatal("a store with no directory misbehaved")
	}
}

func catalogForUsage() *Catalog {
	c := New()
	c.AddAll([]Feature{
		{Name: "/help", Kind: KindCommand, What: "help"},
		{Name: "/verify", Kind: KindCommand, What: "checks"},
		{Name: "/undo", Kind: KindCommand, What: "revert"},
		{Name: "@path", Kind: KindKey, What: "attach"},
		{Name: "ctrl+g", Kind: KindKey, What: "background"},
		{Name: "/goal", Kind: KindCommand, What: "keep going"},
		{Name: "-p", Kind: KindFlag, What: "headless"},
		{Name: "compact_at", Kind: KindConfig, What: "threshold"},
	})
	return c
}

func TestNotTriedNamesWhatIsWorthDiscovering(t *testing.T) {
	u := OpenUsage(t.TempDir())
	u.Note("/verify")
	got := u.NotTried(catalogForUsage(), 5)
	var names []string
	for _, f := range got {
		names = append(names, f.Name)
	}
	for _, unwanted := range []string{"/verify", "/help", "-p", "compact_at"} {
		for _, n := range names {
			if n == unwanted {
				t.Errorf("%s should not be in the not-tried list: %v", unwanted, names)
			}
		}
	}
	if len(names) == 0 || names[0] != "/undo" {
		t.Fatalf("the most useful untried feature was not first: %v", names)
	}
}

func TestNotTriedIsBounded(t *testing.T) {
	u := OpenUsage(t.TempDir())
	if got := u.NotTried(catalogForUsage(), 2); len(got) != 2 {
		t.Fatalf("asked for 2, got %d", len(got))
	}
	if got := u.NotTried(catalogForUsage(), 0); got != nil {
		t.Fatalf("asked for none, got %d", len(got))
	}
	if got := u.NotTried(nil, 5); got != nil {
		t.Fatal("a nil catalogue produced features")
	}
}

func TestSummaryIsOneLineAndEmptyWhenEverythingIsKnown(t *testing.T) {
	u := OpenUsage(t.TempDir())
	line := u.Summary(catalogForUsage())
	if !strings.HasPrefix(line, "not yet tried: ") || strings.Contains(line, "\n") {
		t.Fatalf("summary = %q", line)
	}
	for _, n := range []string{"/verify", "/undo", "@path", "ctrl+g", "/goal"} {
		u.Note(n)
	}
	if line := u.Summary(catalogForUsage()); line != "" {
		t.Fatalf("a user who has used everything was still told: %q", line)
	}
}

func TestTheFileHoldsNothingButCounts(t *testing.T) {
	dir := t.TempDir()
	u := OpenUsage(dir)
	u.Note("/undo")
	if err := u.Flush(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, usageFile))
	if err != nil {
		t.Fatal(err)
	}
	// No identifiers, no timestamps, no paths: a map of names to counts
	// is the whole record, and the test says so out loud because that is
	// the promise being made.
	if got := string(data); got != `{"/undo":1}` {
		t.Fatalf("the usage file holds more than counts: %s", got)
	}
}
