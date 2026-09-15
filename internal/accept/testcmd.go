package accept

import (
	"github.com/xmonader/janetcode/internal/shell"
	"path/filepath"
	"sort"
	"strings"

	"github.com/xmonader/janetcode/internal/textfmt"
)

// InferTestCommand guesses a command that runs files, from the
// project's own verify commands when one already looks like a check, or
// from per-language convention otherwise: go test ./<pkg>/..., pytest
// <files>, npm test, cargo test. It exists as a fallback for a
// test-writing turn that ends with no ACCEPT-VERIFY line — the model is
// still asked to state one first, because it knows which package or
// binary it actually wrote tests for, which this can only guess at from
// the file paths — and is kept to one convention per common ecosystem on
// purpose, so approval is never blocked waiting on a better guess.
func InferTestCommand(files []string, root string, verifyCommands []string) string {
	for _, c := range verifyCommands {
		if shell.IsCheck(c) {
			return c
		}
	}
	exts := map[string]bool{}
	for _, f := range files {
		exts[strings.ToLower(filepath.Ext(f))] = true
	}
	switch {
	case exts[".go"]:
		pkgs := map[string]bool{}
		for _, f := range files {
			if !strings.HasSuffix(f, "_test.go") {
				continue
			}
			pkgs[goPackageOf(f, root)] = true
		}
		if len(pkgs) == 1 {
			for p := range pkgs {
				if p != "" {
					return "go test " + p
				}
			}
		}
		return "go test ./..."
	case exts[".py"]:
		names := make([]string, 0, len(files))
		for _, f := range files {
			names = append(names, textfmt.RelTo(root, f))
		}
		sort.Strings(names)
		return "pytest " + strings.Join(names, " ")
	case exts[".js"], exts[".ts"], exts[".jsx"], exts[".tsx"], exts[".mjs"]:
		return "npm test"
	case exts[".rs"]:
		return "cargo test"
	}
	return ""
}

// goPackageOf is the ./... form of a Go test file's package, relative to
// root, or "" when it cannot be placed under root.
func goPackageOf(f, root string) string {
	dir := filepath.Dir(f)
	if root == "" {
		return ""
	}
	rel, err := filepath.Rel(root, dir)
	if err != nil || strings.HasPrefix(rel, "..") {
		return ""
	}
	if rel == "." {
		return "./..."
	}
	return "./" + filepath.ToSlash(rel) + "/..."
}
