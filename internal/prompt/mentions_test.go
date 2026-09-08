package prompt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExpandMentions(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n"), 0o644)
	os.MkdirAll(filepath.Join(root, "pkg"), 0o755)
	os.WriteFile(filepath.Join(root, "pkg", "b.go"), []byte("package pkg\n"), 0o644)
	os.WriteFile(filepath.Join(root, "bin.dat"), []byte("ab\x00cd"), 0o644)
	big := strings.Repeat("x", MaxMentionBytes+10)
	os.WriteFile(filepath.Join(root, "big.txt"), []byte(big), 0o644)

	text := "look at @a.go, @pkg/ and @missing.go and mail me@example.com @bin.dat @big.txt @a.go"
	got, atts := ExpandMentions(root, text)
	if !strings.HasPrefix(got, text) {
		t.Fatalf("user text altered:\n%s", got)
	}
	if len(atts) != 4 {
		t.Fatalf("attachments = %+v", atts)
	}
	for _, want := range []string{
		`<attached path="a.go">` + "\npackage a\n\n</attached>",
		`<attached path="pkg/">` + "\nb.go\n</attached>",
		"(binary file, 5 bytes)",
		"… truncated, " + "102410 bytes total",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got[len(text):])
		}
	}
	if strings.Contains(got, "missing.go\">") || strings.Contains(got, "example.com") && strings.Contains(got, "<attached path=\"example.com") {
		t.Fatalf("nonexistent path attached:\n%s", got)
	}
	// No mentions: text passes through untouched.
	if plain, atts := ExpandMentions(root, "hello"); plain != "hello" || atts != nil {
		t.Fatalf("plain = %q atts = %v", plain, atts)
	}
}

func TestCompletePath(t *testing.T) {
	root := t.TempDir()
	for _, p := range []string{"main.go", "main_test.go", "Makefile", ".hidden", "internal/x.go", "node_modules/y.js"} {
		os.MkdirAll(filepath.Join(root, filepath.Dir(p)), 0o755)
		os.WriteFile(filepath.Join(root, p), []byte("x"), 0o644)
	}
	got := CompletePath(root, "ma")
	if strings.Join(got, ",") != "main.go,main_test.go" {
		t.Fatalf("got %v", got)
	}
	got = CompletePath(root, "")
	if strings.Join(got, ",") != "Makefile,internal/,main.go,main_test.go" {
		t.Fatalf("got %v", got)
	}
	if got := CompletePath(root, "internal/"); strings.Join(got, ",") != "internal/x.go" {
		t.Fatalf("got %v", got)
	}
	if got := CompletePath(root, ".h"); strings.Join(got, ",") != ".hidden" {
		t.Fatalf("got %v", got)
	}
	if got := CompletePath(root, "nope/"); got != nil {
		t.Fatalf("got %v", got)
	}
}

func TestExpandMentionsImages(t *testing.T) {
	root := t.TempDir()
	png := []byte("\x89PNG\r\n\x1a\nfakepng")
	os.WriteFile(filepath.Join(root, "shot.PNG"), png, 0o644)
	os.WriteFile(filepath.Join(root, "huge.jpg"), make([]byte, MaxImageBytes+1), 0o644)
	text := "look at @shot.PNG and @huge.jpg"
	got, atts := ExpandMentions(root, text)
	if len(atts) != 2 || !atts[0].Image() || atts[0].MediaType != "image/png" || atts[1].Image() {
		t.Fatalf("atts = %+v", atts)
	}
	if strings.Contains(got, "shot.PNG\">") {
		t.Fatalf("image must not be inlined as text:\n%s", got)
	}
	if !strings.Contains(got, "(image not attached: ") || !strings.Contains(got, "exceeds") {
		t.Fatalf("oversize image not reported:\n%s", got)
	}
	blocks := ImageBlocks(atts)
	if len(blocks) != 1 || blocks[0].Type != "image" || blocks[0].Data == "" {
		t.Fatalf("blocks = %+v", blocks)
	}
}
