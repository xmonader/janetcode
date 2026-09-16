package main

import (
	"slices"
	"strings"
	"testing"
)

// A session task's later prompts must continue the session the earlier
// ones left behind. --resume takes janetcode's own session id, and
// sess.id is a uuid this launcher invents for the harnesses that accept
// one — passing it named a session that never existed, so every prompt
// after the first started from nothing and the session set measured a
// harness with no continuation at all.
func TestJanetArgvContinuesASession(t *testing.T) {
	cfg := runConfig{Model: "m", MaxTurns: 40, JanetBin: "/bin/janetcode", RepoRoot: "/repo"}

	first := janetArgv(cfg, "p1", true, 0)
	if slices.Contains(first, "--continue") || slices.Contains(first, "--resume") {
		t.Fatalf("the first prompt of a session has nothing to continue: %v", first)
	}
	later := janetArgv(cfg, "p2", true, 1)
	if !slices.Contains(later, "--continue") {
		t.Fatalf("a later prompt must continue the task's session: %v", later)
	}
	if slices.Contains(later, "--resume") {
		t.Fatalf("--resume takes janetcode's own session id, which this launcher does not have: %v", later)
	}
	single := janetArgv(cfg, "p", false, 0)
	if slices.Contains(single, "--continue") {
		t.Fatalf("a single-prompt task runs fresh: %v", single)
	}
	// The limits are the ones the eval driver passes, so a task that
	// fails on only one driver says something about the harness.
	for _, want := range []string{"--max-turns", "40", "--output-format", "json", "--yolo"} {
		if !slices.Contains(single, want) {
			t.Fatalf("argv is missing %q: %v", want, single)
		}
	}
}

// An empty value in extra unsets the inherited variable rather than
// exporting an empty one (XAI_API_KEY for grok, so it cannot fall back).
func TestChildEnvReplacesAndUnsets(t *testing.T) {
	t.Setenv("HARNESSBENCH_KEEPME", "inherited")
	t.Setenv("HARNESSBENCH_REPLACED", "old")
	t.Setenv("HARNESSBENCH_UNSET", "old")
	env := childEnv(map[string]string{
		"HARNESSBENCH_REPLACED": "new",
		"HARNESSBENCH_UNSET":    "",
	})
	got := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		got[k] = v
	}
	if got["HARNESSBENCH_KEEPME"] != "inherited" {
		t.Fatalf("an untouched variable should be inherited, got %q", got["HARNESSBENCH_KEEPME"])
	}
	if got["HARNESSBENCH_REPLACED"] != "new" {
		t.Fatalf("replaced = %q, want new", got["HARNESSBENCH_REPLACED"])
	}
	if _, ok := got["HARNESSBENCH_UNSET"]; ok {
		t.Fatal("an empty value must unset the variable, not export it empty")
	}
}

// An excerpt is clipped from the end (what a failing harness printed last
// says why it failed) and must stay valid UTF-8: slicing the bytes could
// land inside a rune.
func TestTrimCapKeepsWholeRunes(t *testing.T) {
	s := strings.Repeat("é", 50) // two bytes each
	got := trimCap(s, 11)
	if len(got) > 11 {
		t.Fatalf("trimCap(%d) returned %d bytes", 11, len(got))
	}
	if strings.ContainsRune(got, '�') {
		t.Fatalf("trimCap cut a rune in half: %q", got)
	}
	if !strings.HasSuffix(got, "é") {
		t.Fatalf("trimCap must keep the tail, got %q", got)
	}
	if short := trimCap("  abc  ", 10); short != "abc" {
		t.Fatalf("a short excerpt is returned trimmed and whole, got %q", short)
	}
}
