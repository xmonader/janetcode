package tool

import (
	"strings"
	"testing"
)

func TestNudgeOncePerTool(t *testing.T) {
	n := &Nudge{Loaded: func(string) bool { return false }}
	h := n.Hint("grep -rn foo src")
	if !strings.HasPrefix(h, NudgeMarker+"grep tool") || !strings.Contains(h, "tool_search") {
		t.Fatalf("hint = %q", h)
	}
	if n.Hint("rg foo | head -5") != "" {
		t.Error("second grep hint")
	}
	if !strings.Contains(n.Hint("find . -name '*.go'"), "glob") || !strings.Contains(n.Hint("cat a.go"), "read_file") {
		t.Error("find/cat hints missing")
	}
	for _, c := range []string{"go test ./...", "cat a b | wc -l", "ls"} {
		if (&Nudge{}).Hint(c) != "" {
			t.Errorf("%q hinted", c)
		}
	}
	for _, c := range []string{"cd /w && cat a.go", "cat a.go b.go 2>/dev/null", "cat x.json; echo ---", "cd /w && rg -n foo"} {
		if (&Nudge{}).Hint(c) == "" {
			t.Errorf("%q not hinted", c)
		}
	}
	for _, c := range []string{"cat > a.go <<'EOF'\nx\nEOF", "python3 c.py && cat out.csv | wc -l", "cd /w && go test ./..."} {
		if (&Nudge{}).Hint(c) != "" {
			t.Errorf("%q hinted", c)
		}
	}
	var off *Nudge
	if off.Hint("grep x y") != "" {
		t.Error("nil nudge hinted")
	}
}
