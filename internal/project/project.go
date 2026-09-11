// Package project works out what kind of project a directory holds, so
// that the first session in it can propose something specific instead of
// something generic.
//
// The first run in a new directory is the one moment a user is willing
// to be told about setup, and "you can configure a verification command"
// is not worth their attention. "go test ./... is what this project's
// checks would be, and /verify set runs it after every change" is.
//
// It does no work beyond stat-ing a handful of names and, where a
// Makefile is the only signal, reading its target lines. It runs nothing
// and it writes nothing: proposing a command is not the same as running
// it, and a directory the user has not asked janetcode to manage does
// not get a .janetcode written into it.
package project

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// Project is what a directory looks like.
type Project struct {
	// Kind is the language or build system, in the words a user would
	// use: "Go", "Node", "Python", "Rust", "Make".
	Kind string
	// Verify is the check command worth proposing, or "".
	Verify string
	// Server is the language server for this kind, and Install the one
	// command that gets it. Both empty when there is nothing to suggest.
	Server, Install string
}

// Detect identifies the project in cwd. ok is false when nothing is
// recognised, which is the common case for a directory of notes and no
// reason to say anything at all.
func Detect(cwd string) (Project, bool) {
	has := func(name string) bool {
		_, err := os.Stat(filepath.Join(cwd, name))
		return err == nil
	}
	switch {
	case has("go.mod"):
		return Project{Kind: "Go", Verify: "go test ./...",
			Server: "gopls", Install: "go install golang.org/x/tools/gopls@latest"}, true
	case has("Cargo.toml"):
		return Project{Kind: "Rust", Verify: "cargo test",
			Server: "rust-analyzer", Install: "rustup component add rust-analyzer"}, true
	case has("package.json"):
		p := Project{Kind: "Node", Verify: nodeVerify(cwd),
			Server: "typescript-language-server", Install: "npm i -g typescript-language-server typescript"}
		return p, true
	case has("pyproject.toml"), has("requirements.txt"), has("setup.py"), has("tox.ini"):
		return Project{Kind: "Python", Verify: "pytest",
			Server: "pyright-langserver", Install: "npm i -g pyright"}, true
	case has("Makefile"), has("makefile"), has("GNUmakefile"):
		if target := makeTarget(cwd); target != "" {
			return Project{Kind: "Make", Verify: "make " + target}, true
		}
		return Project{Kind: "Make"}, true
	}
	return Project{}, false
}

// nodeVerify prefers the script the project actually declares. A
// package.json without a test script gets no proposal rather than a
// command that will fail the first time it is run.
func nodeVerify(cwd string) string {
	data, err := os.ReadFile(filepath.Join(cwd, "package.json"))
	if err != nil {
		return ""
	}
	// A full JSON parse would be the right thing if anything depended on
	// the value; all that is wanted is whether a "test" script exists,
	// and the file is the project's, not an attacker's.
	body := string(data)
	i := strings.Index(body, `"scripts"`)
	if i < 0 {
		return ""
	}
	if !strings.Contains(body[i:], `"test"`) {
		return ""
	}
	return "npm test"
}

// makeTarget finds the first of the conventional check targets the
// Makefile declares.
func makeTarget(cwd string) string {
	for _, name := range []string{"Makefile", "makefile", "GNUmakefile"} {
		f, err := os.Open(filepath.Join(cwd, name))
		if err != nil {
			continue
		}
		found := map[string]bool{}
		sc := bufio.NewScanner(f)
		for sc.Scan() && len(found) < 8 {
			line := sc.Text()
			if line == "" || line[0] == '\t' || line[0] == '#' || line[0] == ' ' {
				continue
			}
			name, _, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			found[strings.TrimSpace(name)] = true
		}
		f.Close()
		for _, want := range []string{"test", "check", "ci", "verify"} {
			if found[want] {
				return want
			}
		}
	}
	return ""
}

// Line is the single line the first session prints: what the project
// looks like, the verification command ready to paste, and the language
// server when it is not already there. onPath reports whether a binary
// is available; pass exec.LookPath wrapped, or a stub in a test.
//
// It is one line because the first run is not the moment for a tour, and
// because a paragraph nobody reads is worse than a sentence they do.
func (p Project) Line(onPath func(string) bool) string {
	var parts []string
	if p.Verify != "" {
		parts = append(parts, `/verify set "`+p.Verify+`" runs it after every change and hands failures back to the model`)
	}
	if p.Server != "" && onPath != nil && !onPath(p.Server) {
		parts = append(parts, p.Server+" is not on PATH ("+p.Install+") for diagnostics after edits")
	}
	if len(parts) == 0 {
		return ""
	}
	return p.Kind + " project · " + strings.Join(parts, " · ")
}
