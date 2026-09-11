package tool

import (
	"context"
	"encoding/json"
	"runtime"
	"strings"
	"testing"
)

func TestNoMatchMeaning(t *testing.T) {
	cases := []struct {
		command string
		exit    int
		want    string
	}{
		{"grep -r needle .", 1, "no lines matched"},
		{"rg needle", 1, "no lines matched"},
		{"cat f | grep needle", 1, "no lines matched"},
		{"timeout 5 grep needle f", 1, "no lines matched"},
		{"/usr/bin/grep needle f", 1, "no lines matched"},
		{"LC_ALL=C grep needle f", 1, "no lines matched"},
		{"diff a b", 1, "the inputs differ"},
		{"cmp a b", 1, "the inputs differ"},
		{"test -f missing", 1, "the condition was false"},
		{"[ -f missing ]", 1, "the condition was false"},
		{"grep a f; diff b c", 1, "nothing matched"},
		// exit 2 from grep is a real failure (bad pattern, unreadable file).
		{"grep -r needle .", 2, ""},
		// a plain command's exit 1 is unchanged.
		{"go test ./...", 1, ""},
		{"grep needle f | wc -l", 1, ""},
		// with &&, exit 1 may come from either side; stay conservative.
		{"grep needle f && echo yes", 1, ""},
		{"", 1, ""},
	}
	for _, c := range cases {
		if got := NoMatchMeaning(c.command, c.exit); got != c.want {
			t.Errorf("NoMatchMeaning(%q, %d) = %q, want %q", c.command, c.exit, got, c.want)
		}
	}
}

func TestExitNoteKeepsExitCodeShape(t *testing.T) {
	note := exitNote("grep needle f", 1)
	if !strings.Contains(note, "[exit code 1]") {
		t.Fatalf("note must keep the [exit code N] shape: %q", note)
	}
	if !strings.Contains(note, "no lines matched") {
		t.Fatalf("note must say what exit 1 meant: %q", note)
	}
	if got := exitNote("go build ./...", 2); got != "\n[exit code 2]" {
		t.Fatalf("ordinary exit changed: %q", got)
	}
}

// A grep that found nothing must not come back as a tool error.
func TestBashNoMatchIsNotAnError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX grep")
	}
	dir := t.TempDir()
	b := Bash{Root: dir}
	out, err := b.Run(context.Background(), json.RawMessage(`{"command":"echo hay | grep needle"}`))
	if err != nil {
		t.Fatalf("a no-match grep was reported as an error: %v", err)
	}
	if !strings.Contains(out, "no lines matched") {
		t.Fatalf("result does not explain the exit: %q", out)
	}
}
