package textfmt

import (
	"fmt"
	"strings"
	"testing"
)

func TestTruncateKeepsBothEnds(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 5000; i++ {
		fmt.Fprintf(&sb, "line %04d ok\n", i)
	}
	sb.WriteString("FAIL: TestSomething expected 1 got 2\n")
	out := TruncateTo(sb.String(), 4000)
	if len(out) > 4200 || !strings.HasPrefix(out, "line 0000 ok\n") || !strings.Contains(out, "FAIL: TestSomething") || !strings.Contains(out, "bytes omitted") {
		t.Fatalf("out = %q", out)
	}
	if got := TruncateTo("short", 100); got != "short" {
		t.Fatalf("got %q", got)
	}
}
