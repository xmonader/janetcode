//go:build unix

package prompt

import (
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Opening a FIFO for reading blocks until a writer appears, so an @ on
// one (or on one named like an image) hung the prompt forever. A path
// that is neither a regular file nor a directory is named, not read.
func TestMentionOfAFifoIsNotRead(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"pipe", "pipe.png"} {
		if err := syscall.Mkfifo(filepath.Join(root, name), 0o644); err != nil {
			t.Skipf("mkfifo: %v", err)
		}
	}
	done := make(chan string, 1)
	go func() {
		got, _ := ExpandMentions(root, "see @pipe and @pipe.png")
		done <- got
	}()
	select {
	case got := <-done:
		if strings.Count(got, "not a regular file") != 2 {
			t.Fatalf("got:\n%s", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ExpandMentions blocked opening a FIFO")
	}
}
