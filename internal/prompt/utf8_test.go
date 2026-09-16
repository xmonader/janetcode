package prompt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// An instruction file longer than the per-file cap is cut to fit. The cut
// must land on a rune boundary: the text goes straight into the system
// prompt, and half a character there is invalid UTF-8 in every request the
// session makes.
func TestOversizedInstructionsStayValidUTF8(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	// Three-byte runes divide neither the 32 KB file cap nor the 64 KB
	// total, so a byte-aligned cut is guaranteed to split one.
	body := strings.Repeat("中", maxInstructionBytes) // 3x the cap
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	ins := Instructions(dir)
	if len(ins) != 1 {
		t.Fatalf("read %d instruction files, want 1", len(ins))
	}
	if !utf8.ValidString(ins[0].Text) {
		t.Fatal("truncated instruction text is not valid UTF-8")
	}
	if !strings.HasSuffix(ins[0].Text, "(truncated)") {
		t.Fatalf("text does not end in the truncation marker: %q", ins[0].Text[len(ins[0].Text)-40:])
	}
	if !utf8.ValidString(System(dir)) {
		t.Fatal("system prompt is not valid UTF-8")
	}
}

// An @import whose file overruns the remaining budget is cut the same way.
func TestOversizedImportStaysValidUTF8(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "big.md"), []byte(strings.Repeat("中", maxInstructionTotal)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("read this:\n@big.md\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ins := Instructions(dir)
	if len(ins) != 1 {
		t.Fatalf("read %d instruction files, want 1", len(ins))
	}
	if !utf8.ValidString(ins[0].Text) {
		t.Fatal("expanded import is not valid UTF-8")
	}
}
