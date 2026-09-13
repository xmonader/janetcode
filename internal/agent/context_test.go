package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPinTextPassesThroughPlainText(t *testing.T) {
	got, err := PinText(t.TempDir(), "remember this")
	if err != nil || got != "remember this" {
		t.Fatalf("got %q err=%v", got, err)
	}
}

func TestPinTextLoadsAtPath(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := PinText(dir, "@notes.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "File notes.txt as of pinning:\n") || !strings.Contains(got, "hello") {
		t.Fatalf("got %q", got)
	}
}

func TestPinTextTruncatesLargeFiles(t *testing.T) {
	dir := t.TempDir()
	big := strings.Repeat("x", maxPinFile+100)
	if err := os.WriteFile(filepath.Join(dir, "big.txt"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := PinText(dir, "@big.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(got, "… (truncated)") {
		t.Fatalf("not truncated: %q", got[len(got)-40:])
	}
}

func TestPinTextWithSpaceIsNotAPath(t *testing.T) {
	// "@" followed by a space is a note that happens to start with @, not
	// a file reference.
	got, err := PinText(t.TempDir(), "@ mention someone")
	if err != nil || got != "@ mention someone" {
		t.Fatalf("got %q err=%v", got, err)
	}
}

func TestPinTextMissingFile(t *testing.T) {
	if _, err := PinText(t.TempDir(), "@nope.txt"); err == nil {
		t.Fatal("want error for a missing file")
	}
}
