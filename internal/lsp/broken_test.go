package lsp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A server can break its own stream — a panic, a stray print on stdout, a
// frame past the size limit — and keep running. The reader stops, but
// nothing else notices: every query then waited out its whole deadline
// against a server that could no longer answer, and said only that it had
// not answered in time.
func TestAQueryFailsAtOnceWhenTheServersStreamBreaks(t *testing.T) {
	root := t.TempDir()
	m := New(root, fakeConfig(t, "--garbage-after-open"))
	defer m.Close()
	if err := os.WriteFile(filepath.Join(root, "a.foo"), []byte("fine\nthing\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	_, err := m.Hover(context.Background(), "a.foo", Point{Line: 1})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("the query succeeded against a server whose stream had broken")
	}
	if !strings.Contains(err.Error(), "Content-Length") {
		t.Fatalf("err = %v, want the reason the stream broke", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("waited %s for a server that could not answer", elapsed)
	}
}
