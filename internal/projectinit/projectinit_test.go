package projectinit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestGenerateFromWhatTheRepositoryShows is the whole claim: the file
// says what is actually here, in each of the shapes a first session
// meets.
func TestGenerateFromWhatTheRepositoryShows(t *testing.T) {
	for _, tc := range []struct {
		name       string
		setup      func(t *testing.T, dir string)
		want, miss []string
	}{
		{
			name: "a Go repository",
			setup: func(t *testing.T, dir string) {
				write(t, dir, "go.mod", "module example.com/x\n\ngo 1.24\n")
				write(t, dir, "Makefile", "check: test lint\n\ttrue\n\ntest:\n\tgo test ./...\n\nVERSION = 1\n")
				write(t, dir, "cmd/app/main.go", "package main\n")
				write(t, dir, "internal/store/store.go", "package store\n")
				write(t, dir, "internal/store/store_test.go", "package store\n")
				write(t, dir, "README.md", "# x\n")
				write(t, dir, "vendor/other/huge.go", "package other\n")
				write(t, dir, "node_modules/dep/index.js", "1\n")
			},
			want: []string{"Go project", "3 Go files", "`make check`", "`go test ./...`", "`internal/` — 2 files", "`cmd/` — 1 files", "## Conventions"},
			miss: []string{"vendor", "node_modules", "npm"},
		},
		{
			name: "a node repository",
			setup: func(t *testing.T, dir string) {
				write(t, dir, "package.json", `{"name":"x","scripts":{"test":"vitest","build":"tsc","dev":"vite"}}`)
				write(t, dir, "src/index.ts", "export {}\n")
				write(t, dir, "src/app.tsx", "export {}\n")
			},
			want: []string{"Node project", "2 TypeScript files", "`npm test`", "`npm run build`", "`src/` — 2 files"},
			miss: []string{"go test", "cargo"},
		},
		{
			name:  "an empty directory",
			setup: func(t *testing.T, dir string) {},
			want:  []string{"An empty directory", "None declared", "/verify set"},
			miss:  []string{"## Layout"},
		},
		{
			name: "a repository that already says something",
			setup: func(t *testing.T, dir string) {
				write(t, dir, "go.mod", "module example.com/x\n")
				write(t, dir, "AGENTS.md", "# House rules\n\n## Testing\n\nrun make check\n")
			},
			want: []string{"Already written down", "`AGENTS.md`", "House rules, Testing", "Do not repeat it here"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			tc.setup(t, dir)
			got := Generate(dir)
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("generated file does not mention %q:\n%s", want, got)
				}
			}
			for _, miss := range tc.miss {
				if strings.Contains(got, miss) {
					t.Errorf("generated file should not mention %q:\n%s", miss, got)
				}
			}
			// A page, not a dump.
			if n := strings.Count(got, "\n"); n > 60 {
				t.Errorf("generated file is %d lines; it is meant to be a page:\n%s", n, got)
			}
		})
	}
}

// TestNeverOverwritesWithoutConfirmation is the rule the command is
// under: the file on disk is the user's.
func TestNeverOverwritesWithoutConfirmation(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "go.mod", "module example.com/x\n")

	res, err := Run(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Error != "" || len(res.Notes) != 1 || !strings.HasPrefix(res.Notes[0], "wrote ") {
		t.Fatalf("first run did not write the file: %+v", res)
	}
	mine := "# mine\n\nhand written\n"
	write(t, dir, File, mine)

	res, err = Run(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Error, "already exists") {
		t.Errorf("a second run should refuse: %+v", res)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, File)); string(got) != mine {
		t.Fatalf("the file was overwritten:\n%s", got)
	}
	if len(res.Tips) == 0 || !strings.Contains(res.Tips[0], "force") {
		t.Errorf("the refusal does not say how to go ahead: %+v", res.Tips)
	}

	if _, err := Run(dir, true); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, File))
	if string(got) == mine || !strings.Contains(string(got), "## Commands") {
		t.Fatalf("force did not replace the file:\n%s", got)
	}
}

// TestFileNameFollowsTheLoader keeps the generated file and the file the
// prompt reads from drifting apart.
func TestFileNameFollowsTheLoader(t *testing.T) {
	if File != "JANETCODE.md" {
		t.Errorf("File = %q", File)
	}
}
