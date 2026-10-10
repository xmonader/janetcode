package prompt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// kernelCheckout writes one project's instruction file into a fresh
// directory: a checkout of the same project somewhere else.
func kernelCheckout(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("Run make test before finishing.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// The point of the kernel: two sessions on the same project in different
// directories send the same system prompt, where the usual one differs
// at the working-directory line. That line, the date and the branch all
// go to EnvironmentNote, for the end of the first message.
func TestKernelPromptIsByteIdenticalAcrossSessions(t *testing.T) {
	a, b := kernelCheckout(t), kernelCheckout(t)
	const m = "[repository map of the committed tree abc]\nmain.go: main\n"
	for _, o := range []Override{{}, {Base: Headless}, {Append: "also be brief"}} {
		ka, kb := KernelSystem(a, o, m), KernelSystem(b, o, m)
		if ka != kb {
			t.Fatalf("kernel prompts differ between checkouts:\n%s\n---\n%s", ka, kb)
		}
		if SystemWith(a, o) == SystemWith(b, o) {
			t.Fatal("the usual prompts were already identical; the test proves nothing")
		}
		for _, volatile := range []string{a, "Date:", "Working directory", EnvironmentHeading} {
			if strings.Contains(ka, volatile) {
				t.Fatalf("kernel prompt carries %q:\n%s", volatile, ka)
			}
		}
		for _, kept := range []string{"Run make test before finishing.", "main.go: main"} {
			if !strings.Contains(ka, kept) {
				t.Fatalf("kernel prompt lost %q:\n%s", kept, ka)
			}
		}
	}
	note := EnvironmentNote(a)
	if !strings.Contains(note, "Working directory: "+a) || !strings.Contains(note, "Date: ") {
		t.Fatalf("the environment note lost the volatile lines: %q", note)
	}
}

func TestKernelSectionOrder(t *testing.T) {
	dir := kernelCheckout(t)
	got := names(KernelSections(dir, Override{Append: "x"}, "map"))
	want := []string{SectionBase, SectionHarness, SectionProject, SectionKernel, SectionAppend}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("kernel order = %v, want %v", got, want)
	}
	// No map: the environment still leaves, nothing takes its place.
	got = names(KernelSections(dir, Override{}, "  \n"))
	want = []string{SectionBase, SectionHarness, SectionProject}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("kernel order without a map = %v, want %v", got, want)
	}
	// The head still comes first, so the build-wide cache entry is
	// shared with sessions that do not use the kernel at all.
	usual := SystemWith(dir, Override{})
	kern := KernelSystem(dir, Override{}, "map")
	head := baseInstructions + Harness
	if !strings.HasPrefix(usual, head) || !strings.HasPrefix(kern, head) {
		t.Fatal("the kernel prompt does not start with the same head as the usual one")
	}
}
