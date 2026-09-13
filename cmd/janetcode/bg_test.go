package main

import (
	"strings"
	"testing"
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
