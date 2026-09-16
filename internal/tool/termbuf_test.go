package tool

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// A persistent shell lives for the whole session, so its output has to be
// bounded: one `yes` used to grow the buffer until the process died of it.
func TestTermBufferIsBounded(t *testing.T) {
	var b termBuffer
	chunk := strings.Repeat("x", 4096)
	for i := 0; i < 200; i++ { // 800 KiB through a 256 KiB window
		if _, err := b.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(b.String()); got > maxTermOutput {
		t.Fatalf("buffer holds %d bytes, want at most %d", got, maxTermOutput)
	}
	if want := int64(200 * 4096); b.Len() != want {
		t.Fatalf("Len = %d, want the absolute %d", b.Len(), want)
	}
}

// Send takes a mark before writing a command and asks for everything
// since. The mark is absolute, so it still means the same thing after
// the window has slid past it.
func TestTermBufferSinceSurvivesTheWindowSliding(t *testing.T) {
	var b termBuffer
	b.Write([]byte("early output"))
	mark := b.Len()
	b.Write([]byte("the command's own output"))
	if got := b.Since(mark); got != "the command's own output" {
		t.Fatalf("Since(mark) = %q", got)
	}
	// Push the mark out of the window entirely.
	b.Write([]byte(strings.Repeat("y", maxTermOutput+10)))
	got := b.Since(mark)
	if strings.Contains(got, "early output") {
		t.Fatalf("output from before the mark came back: %q", got[:40])
	}
	if len(got) > maxTermOutput {
		t.Fatalf("Since returned %d bytes, more than the window", len(got))
	}
	// An offset past the end is simply nothing, not a panic.
	if got := b.Since(b.Len() + 1000); got != "" {
		t.Fatalf("Since(past the end) = %q", got)
	}
	if got := b.Since(0); len(got) > maxTermOutput {
		t.Fatalf("Since(0) = %d bytes", len(got))
	}
}

// The window slides by whole characters: a cut through a multi-byte rune
// would put a replacement char in front of output the model then reads.
func TestTermBufferSlidesOnRuneBoundaries(t *testing.T) {
	var b termBuffer
	b.Write([]byte(strings.Repeat("é", maxTermOutput)))
	if s := b.String(); !utf8.ValidString(s) {
		t.Fatal("the window cut a rune in half")
	}
}
