package headless

import (
	"bytes"
	"testing"
)

// Headless kept a tool's carriage returns and bells: a progress line
// printed over itself mid-line in the caller's terminal, and a bell rang
// it. Both are laid out as a terminal would show them now.
func TestSafeLaysOutControls(t *testing.T) {
	if got := safe("AAAA 10%\rBB\a"); got != "BBAA 10%" {
		t.Fatalf("safe = %q", got)
	}
	var b bytes.Buffer
	if _, err := (plainWriter{&b}).Write([]byte("ok\a\b done\r\n")); err != nil {
		t.Fatal(err)
	}
	if b.String() != "ok done\n" {
		t.Fatalf("plain writer = %q", b.String())
	}
}
