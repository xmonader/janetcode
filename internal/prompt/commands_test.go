package prompt

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCustomCommands(t *testing.T) {
	cwd, cfg := t.TempDir(), t.TempDir()
	write := func(dir, name, body string) {
		os.MkdirAll(filepath.Join(dir, "commands"), 0o755)
		os.WriteFile(filepath.Join(dir, "commands", name), []byte(body), 0o644)
	}
	write(cfg, "review.md", "# Review the diff\nReview: $ARGUMENTS carefully.")
	write(cfg, "shadowed.md", "global")
	write(filepath.Join(cwd, ".janetcode"), "shadowed.md", "project version")
	write(filepath.Join(cwd, ".janetcode"), "notes.txt", "ignored")
	write(filepath.Join(cwd, ".janetcode"), "empty.md", "  \n")

	cmds := CustomCommands(cwd, cfg)
	if len(cmds) != 2 || cmds[0].Name != "review" || cmds[1].Name != "shadowed" {
		t.Fatalf("cmds = %+v", cmds)
	}
	if cmds[0].Help != "Review the diff" {
		t.Fatalf("help = %q", cmds[0].Help)
	}
	if got := cmds[0].Expand("  main.go "); got != "# Review the diff\nReview: main.go carefully." {
		t.Fatalf("expand = %q", got)
	}
	if got := cmds[1].Expand("extra"); got != "project version\n\nextra" {
		t.Fatalf("shadow/append = %q", got)
	}
	if got := cmds[1].Expand(""); got != "project version" {
		t.Fatalf("no args = %q", got)
	}
	if got := CustomCommands(t.TempDir(), ""); len(got) != 0 {
		t.Fatalf("expected none, got %+v", got)
	}
}
