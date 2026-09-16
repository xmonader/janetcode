package tool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/textfmt"
)

// A model can run `yes`. The buffer that collects a command's output has
// to stay bounded, and — since the result is clipped to MaxOutput anyway,
// head and tail — the clipped result must be the same bytes an unbounded
// buffer would have produced.
func TestSwapBufferIsBoundedAndKeepsBothEnds(t *testing.T) {
	var s swapBuffer
	var whole strings.Builder
	line := strings.Repeat("a", 999) + "\n"
	for i := 0; i < 6000; i++ { // ~6 MB through a 2 MB window
		s.Write([]byte(line))
		whole.WriteString(line)
	}
	got := s.String()
	if len(got) > 2*swapEnd+200 {
		t.Fatalf("buffer holds %d bytes, want about %d", len(got), 2*swapEnd)
	}
	if !strings.Contains(got, "bytes of output dropped") {
		t.Fatal("the dropped middle was not reported")
	}
	// What the model is shown is identical either way.
	want := textfmt.Truncate(whole.String())
	if head, wantHead := got[:1000], want[:1000]; head != wantHead {
		t.Fatalf("head differs:\n got %q\nwant %q", head[:60], wantHead[:60])
	}
	if got, want := textfmt.Truncate(got), want; got[len(got)-1000:] != want[len(want)-1000:] {
		t.Fatal("tail differs from what the unbounded buffer would have shown")
	}
}

// Short output is untouched: no marker, no reshaping.
func TestSwapBufferLeavesShortOutputAlone(t *testing.T) {
	var s swapBuffer
	s.Write([]byte("hello "))
	s.Write([]byte("world"))
	if got := s.String(); got != "hello world" {
		t.Fatalf("String() = %q", got)
	}
}

// Promotion hands the file everything still held, and from then on the
// command writes straight to it.
func TestSwapBufferAttachWritesWhatItHeld(t *testing.T) {
	var s swapBuffer
	s.Write([]byte("before\n"))
	p := filepath.Join(t.TempDir(), "job.log")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := s.attach(f); err != nil {
		t.Fatal(err)
	}
	s.Write([]byte("after\n"))
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "before\nafter\n" {
		t.Fatalf("log = %q", data)
	}
	if got := s.String(); got != "" {
		t.Fatalf("an attached buffer should hold nothing, got %q", got)
	}
}

// io.Writer's contract: Write reports how many bytes of p it took, and it
// takes all of them however little it keeps. A short count makes io.Copy
// — which is what os/exec pipes output through — stop with ErrShortWrite.
func TestSwapBufferWriteReportsEveryByte(t *testing.T) {
	var s swapBuffer
	big := make([]byte, swapEnd+7)
	for _, p := range [][]byte{[]byte("x"), big, big, big} {
		n, err := s.Write(p)
		if err != nil {
			t.Fatal(err)
		}
		if n != len(p) {
			t.Fatalf("Write(%d bytes) = %d", len(p), n)
		}
	}
}
