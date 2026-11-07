package prompt

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
)

// headlessSum is the SHA-256 of Headless as it stood before HeadlessWith
// existed. The base prompt is what a benchmark's results were measured
// with; a change to it is a deliberate act, which changes this sum.
const headlessSum = "0a3e9dc1b35991e6b7dd87dd5ec62912c3b7bf2f0526447caf74b133337d475e"

func TestHeadlessBaseIsUnchanged(t *testing.T) {
	if got := fmt.Sprintf("%x", sha256.Sum256([]byte(Headless))); got != headlessSum {
		t.Fatalf("Headless changed: sha256 %s", got)
	}
}

// With both behaviours off the prompt is the base, byte for byte.
func TestHeadlessWithSwitchedOffIsTheBase(t *testing.T) {
	if got := HeadlessWith(false, ""); got != Headless {
		t.Fatalf("off prompt differs:\n%s", got)
	}
}

func TestHeadlessWithAutocheck(t *testing.T) {
	got := HeadlessWith(true, "")
	if !strings.Contains(got, "the harness reruns that same command after each of your later edits") || !strings.Contains(got, "inline command, not in a file") {
		t.Fatalf("prompt = %s", got)
	}
}

func TestHeadlessWithScratchDirNamesItAndDropsTheInlineRule(t *testing.T) {
	got := HeadlessWith(false, "/home/u/.cache/janetcode/checks/s1")
	if strings.Contains(got, "not in a file") || !strings.Contains(got, "one small file in /home/u/.cache/janetcode/checks/s1 ") || !strings.Contains(got, "change it with edit_file instead of writing it again") {
		t.Fatalf("prompt = %s", got)
	}
	if strings.Contains(got, "inline assert") || strings.Contains(got, "harness reruns") {
		t.Fatalf("prompt = %s", got)
	}
	// Every sentence this rewrites is still in the base: a reworded base
	// must not turn the rewrite into a silent no-op.
	for _, s := range []string{headlessInlineRule, headlessTestRule, headlessAssertRule} {
		if strings.Count(Headless, s) != 1 {
			t.Fatalf("%q is not once in Headless", s)
		}
	}
}
