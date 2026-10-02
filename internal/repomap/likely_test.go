package repomap

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestLikelyPrefersFilesTheRequestNames(t *testing.T) {
	root := t.TempDir()
	write := func(p, s string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(root, filepath.Dir(p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, p), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module x\n")
	write("ledger/balance.go", "package ledger\n\nfunc Balance() int { return 0 }\n")
	write("util/strings.go", "package util\n\nfunc Pad(s string) string { return s }\n")
	write("util/more.go", "package util\n\nfunc Trim(s string) string { return Pad(s) }\n")
	gotRoot, paths := Likely(context.Background(), &Cache{Root: root}, "Balance returns the wrong total in the ledger", 2)
	if gotRoot == "" || len(paths) == 0 || paths[0] != "ledger/balance.go" {
		t.Fatalf("root %q paths %v", gotRoot, paths)
	}
	if _, none := Likely(context.Background(), &Cache{Root: root}, "  ", 2); none != nil {
		t.Fatalf("an empty request named files: %v", none)
	}
}
