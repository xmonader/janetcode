package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The covering test's name is left out when the hint is switched off.
func TestEditFeedbackTestHintSwitch(t *testing.T) {
	dir := t.TempDir()
	for p, c := range map[string]string{"calc.py": "x = 1\n", "test_calc.py": "def test_x(): pass\n"} {
		if err := os.WriteFile(filepath.Join(dir, p), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	abs := filepath.Join(dir, "calc.py")
	if got := editFeedback(context.Background(), abs, true); !strings.Contains(got, "test_calc.py") {
		t.Fatalf("hint missing: %q", got)
	}
	if got := editFeedback(context.Background(), abs, false); strings.Contains(got, "Tests covering") {
		t.Fatalf("hint not switched off: %q", got)
	}
}
