package verify

import (
	"strings"
	"testing"
)

func TestCaptureKeepsBothEndsAndReportsTheRest(t *testing.T) {
	var c capture
	if n, err := c.Write([]byte("start\n")); n != 6 || err != nil {
		t.Fatalf("Write = %d, %v", n, err)
	}
	line := strings.Repeat("b", 1023) + "\n"
	for i := 0; i < 2000; i++ { // ~2 MB through a 1 MB window
		if n, _ := c.Write([]byte(line)); n != len(line) {
			t.Fatalf("Write took %d of %d bytes", n, len(line))
		}
	}
	c.Write([]byte("END\n"))
	got := c.String()
	if !strings.HasPrefix(got, "start\n") {
		t.Fatal("the head was lost")
	}
	if !strings.HasSuffix(got, "END\n") {
		t.Fatal("the tail was lost")
	}
	if !strings.Contains(got, "bytes omitted") {
		t.Fatal("the dropped middle was not reported")
	}
	if len(got) > 2*maxEnd+200 {
		t.Fatalf("kept %d bytes, want about %d", len(got), 2*maxEnd)
	}
}

func TestCaptureLeavesShortOutputAlone(t *testing.T) {
	var c capture
	c.Write([]byte("ok\n"))
	if got := c.String(); got != "ok\n" {
		t.Fatalf("String() = %q", got)
	}
}
