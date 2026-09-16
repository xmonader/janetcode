package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/headless"
)

func TestBackgroundChildArgs(t *testing.T) {
	got := backgroundChildArgs("sess-1", "write hi", []string{"/tmp/a", "/tmp/b"})
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, "--resume sess-1") || !strings.Contains(joined, "-p write hi") || !strings.Contains(joined, "--yolo") {
		t.Fatalf("args = %v", got)
	}
	if strings.Contains(joined, "--bg") {
		t.Fatal("child must not see --bg")
	}
	if !strings.Contains(joined, "--add-dir /tmp/a") || !strings.Contains(joined, "--add-dir /tmp/b") {
		t.Fatalf("extra dirs missing: %v", got)
	}
}

// The background child runs with its stdin closed, so `git diff |
// janetcode --bg -p review` only works if the parent folds the pipe into
// the prompt it hands over. Too much for a command line is an error, not
// a silent half-review.
func TestBgPromptCarriesPipedStdin(t *testing.T) {
	got, err := bgPrompt("review this", "")
	if err != nil || got != "review this" {
		t.Fatalf("no stdin: %q, %v", got, err)
	}

	got, err = bgPrompt("review this", "diff --git a/x b/x")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "review this") || !strings.Contains(got, "<stdin>\ndiff --git a/x b/x\n</stdin>") {
		t.Fatalf("stdin not folded in: %q", got)
	}

	if _, err := bgPrompt("review", strings.Repeat("x", maxBackgroundStdin+1)); err == nil {
		t.Fatal("oversized stdin was accepted")
	} else if !errors.Is(err, headless.ErrBadInput) {
		t.Fatalf("error is not a usage error: %v", err)
	}
}
