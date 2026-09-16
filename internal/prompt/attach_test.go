package prompt

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestAttachedFileTruncatesOnARuneBoundary: the cut used to land in the
// middle of a multi-byte character, which is invalid UTF-8 and is
// rejected by providers outright.
func TestAttachedFileTruncatesOnARuneBoundary(t *testing.T) {
	dir := t.TempDir()
	// "é" is two bytes, so one of the two paddings below puts a rune
	// boundary an odd number of bytes from the cut whatever its parity.
	for _, pad := range []int{0, 1} {
		name := fmt.Sprintf("big%d.txt", pad)
		body := strings.Repeat("a", pad) + strings.Repeat("é", MaxMentionBytes)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		out, atts := ExpandMentions(dir, "look at @"+name)
		if len(atts) != 1 {
			t.Fatalf("pad=%d: want one attachment, got %d", pad, len(atts))
		}
		if !utf8.ValidString(out) {
			t.Errorf("pad=%d: the attached text is not valid UTF-8", pad)
		}
		if !strings.Contains(out, "truncated") {
			t.Errorf("pad=%d: the attachment does not say it was truncated", pad)
		}
	}
}

// TestAttachedFileIsReadWhole guards the short-read bug: one Read may
// return less than the buffer holds, which silently attached a prefix of
// the file as if it were the whole of it.
func TestAttachedFileIsReadWhole(t *testing.T) {
	dir := t.TempDir()
	body := strings.Repeat("0123456789abcdef", 8192) // 128 KiB, over the cap
	if err := os.WriteFile(filepath.Join(dir, "whole.txt"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _ := ExpandMentions(dir, "@whole.txt")
	if !strings.Contains(out, "\n… truncated") {
		t.Fatal("a file over the cap must say it was truncated")
	}
	if len(out) < MaxMentionBytes {
		t.Errorf("attached %d bytes, want the full %d-byte cap", len(out), MaxMentionBytes)
	}
}

// TestAttachedFileUnderTheCapIsWhole is the ordinary case: nothing is
// cut and nothing claims to have been.
func TestAttachedFileUnderTheCapIsWhole(t *testing.T) {
	dir := t.TempDir()
	body := strings.Repeat("λ", 1000)
	if err := os.WriteFile(filepath.Join(dir, "small.txt"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _ := ExpandMentions(dir, "@small.txt")
	if !strings.Contains(out, body) {
		t.Error("the whole file should be attached")
	}
	if strings.Contains(out, "truncated") {
		t.Error("a file under the cap must not be reported as truncated")
	}
}
