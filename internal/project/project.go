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
	"encoding/json"
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

// kind is one recognisable project, in the order they are tried: the
// first whose marker files are present wins, so a Go repository with a
// Makefile is a Go project.
type kind struct {
	// markers are the file names any one of which identifies this kind.
	markers []string
	proj    Project
	// verify, when set, overrides proj.Verify by looking at the project
	// itself. It returns "" when there is no command worth proposing.
	verify func(cwd string) string
}

var kinds = []kind{
	{markers: []string{"go.mod"}, proj: Project{Kind: "Go", Verify: "go test ./...",
		Server: "gopls", Install: "go install golang.org/x/tools/gopls@latest"}},
	{markers: []string{"Cargo.toml"}, proj: Project{Kind: "Rust", Verify: "cargo test",
		Server: "rust-analyzer", Install: "rustup component add rust-analyzer"}},
	{markers: []string{"package.json"}, proj: Project{Kind: "Node",
		Server: "typescript-language-server", Install: "npm i -g typescript-language-server typescript"},
		verify: nodeVerify},
	{markers: []string{"pyproject.toml", "requirements.txt", "setup.py", "tox.ini"},
		proj: Project{Kind: "Python", Verify: "pytest",
			Server: "pyright-langserver", Install: "npm i -g pyright"}},
	{markers: []string{"Makefile", "makefile", "GNUmakefile"}, proj: Project{Kind: "Make"},
		verify: makeVerify},
}

// hasFile reports whether cwd holds a readable regular file (or a link to
// one) by that name. A *directory* called go.mod is not a Go module.
func hasFile(cwd, name string) bool {
	info, err := os.Stat(filepath.Join(cwd, name))
	return err == nil && info.Mode().IsRegular()
}

// Detect identifies the project in cwd. ok is false when nothing is
// recognised, which is the common case for a directory of notes and no
// reason to say anything at all.
func Detect(cwd string) (Project, bool) {
	for _, k := range kinds {
		found := false
		for _, m := range k.markers {
			if hasFile(cwd, m) {
				found = true
				break
			}
		}
		if !found {
			continue
		}
		p := k.proj
		if k.verify != nil {
			p.Verify = k.verify(cwd)
		}
		return p, true
	}
	return Project{}, false
}

// nodeVerify prefers the script the project actually declares. A
// package.json without a test script gets no proposal rather than a
// command that will fail the first time it is run — so the file is
// parsed rather than grepped: a "test" key somewhere else in it (a
// dependency, a workspace entry) is not a test script.
func nodeVerify(cwd string) string {
	data, err := os.ReadFile(filepath.Join(cwd, "package.json"))
	if err != nil {
		return ""
	}
	var pkg struct {
		Scripts map[string]json.RawMessage `json:"scripts"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return ""
	}
	if _, ok := pkg.Scripts["test"]; !ok {
		return ""
	}
	return "npm test"
}

// makeVerify proposes the conventional check target the Makefile
// declares, preferring test over check over ci over verify. An empty
// string means the Makefile declares none of them.
func makeVerify(cwd string) string {
	if target := makeTarget(cwd); target != "" {
		return "make " + target
	}
	return ""
}

const (
	// maxMakeLines bounds the read of a generated or hostile Makefile.
	maxMakeLines = 5000
	// maxMakeLine bounds one line, since bufio.Scanner would otherwise
	// stop at its 64KiB default and silently report no targets.
	maxMakeLine = 256 * 1024
)

// wantedTargets are the conventional check targets, in preference order.
var wantedTargets = []string{"test", "check", "ci", "verify"}

func makeTarget(cwd string) string {
	for _, name := range []string{"Makefile", "makefile", "GNUmakefile"} {
		if !hasFile(cwd, name) {
			continue
		}
		if t := scanMakefile(filepath.Join(cwd, name)); t != "" {
			return t
		}
	}
	return ""
}

func scanMakefile(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	found := map[string]bool{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), maxMakeLine)
	for n := 0; n < maxMakeLines && sc.Scan(); n++ {
		for _, t := range targetNames(sc.Text()) {
			found[t] = true
		}
	}
	for _, want := range wantedTargets {
		if found[want] {
			return want
		}
	}
	return ""
}

// targetNames returns the targets one Makefile line declares, if it
// declares any. Recipe lines (a leading tab), comments, continuations
// and variable assignments (FOO := x, FOO = x) declare none, and a
// special target (.PHONY) is not one a user can run.
func targetNames(line string) []string {
	line = strings.TrimRight(line, "\r")
	if line == "" || line[0] == '\t' || line[0] == '#' || line[0] == ' ' {
		return nil
	}
	if i := strings.IndexByte(line, '#'); i >= 0 {
		line = line[:i]
	}
	colon := strings.IndexByte(line, ':')
	if colon < 0 {
		return nil
	}
	// An assignment: either the "=" comes first (FOO = x) or it follows
	// the colon immediately (FOO := x, FOO ::= x).
	if eq := strings.IndexByte(line, '='); eq >= 0 && eq < colon {
		return nil
	}
	rest := strings.TrimLeft(line[colon:], ":")
	if strings.HasPrefix(rest, "=") {
		return nil
	}
	var out []string
	for _, name := range strings.Fields(line[:colon]) {
		if strings.HasPrefix(name, ".") {
			continue
		}
		out = append(out, name)
	}
	return out
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
		parts = append(parts, "/verify set "+p.Verify+" runs it after every change and hands failures back to the model")
	}
	if p.Server != "" && onPath != nil && !onPath(p.Server) {
		parts = append(parts, p.Server+" is not on PATH ("+p.Install+") for diagnostics after edits")
	}
	if len(parts) == 0 {
		return ""
	}
	return p.Kind + " project · " + strings.Join(parts, " · ")
}
