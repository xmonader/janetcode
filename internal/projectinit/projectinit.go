// Package projectinit writes the project's first instruction file from
// what the repository actually shows.
//
// A session in a new repository starts with a first-run hint and
// whatever memory exists, which is to say with nothing about the
// project. The file that would fix that — JANETCODE.md, read at the top
// of every session — is one nobody writes, because writing it from a
// blank page means deciding what a stranger needs to know. Deciding that
// is hard; listing what the repository already says is not, and it is
// most of the value: the languages, the commands that exist, the shape
// of the tree. What is left after that is the judgement, and the file
// says where to put it.
//
// It stays a page. A dump of every directory and every script is a file
// the model pays for in every request and nobody corrects when it goes
// stale.
package projectinit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/xmonader/janetcode/internal/cmdresult"
	"github.com/xmonader/janetcode/internal/project"
	"github.com/xmonader/janetcode/internal/prompt"
	"github.com/xmonader/janetcode/internal/textfmt"
	"github.com/xmonader/janetcode/internal/verify"
)

// File is the instruction file this writes: the first name the project
// instructions loader looks for, so the two can never disagree.
var File = prompt.InstructionFiles[0]

// maxScan bounds the walk. A repository with more files than this is one
// where the counts are already decided by the first twenty thousand.
const maxScan = 20000

// Survey is what the repository shows about itself.
type Survey struct {
	Root string
	Name string
	// Kind is project.Detect's word for it ("Go", "Node"), or "".
	Kind string
	// Languages are the file counts by language, most first.
	Languages []Count
	// Dirs are the top-level directories with their file counts, most
	// first.
	Dirs []Count
	// Commands are the commands the repository declares, in the order
	// they are worth trying.
	Commands []Command
	// Existing are the other instruction files already here (AGENTS.md,
	// CLAUDE.md), with their headings, which this file must not repeat.
	Existing []Instruction
	// Files is how many files were counted.
	Files int
}

// Count is one label and how many files carry it.
type Count struct {
	Name string
	N    int
}

// Command is one command the project declares and where it came from.
type Command struct {
	Run  string
	From string
}

// Instruction is another instruction file that is already read.
type Instruction struct {
	Name     string
	Headings []string
}

// skipDirs are never walked: they are somebody else's code, or build
// output, and their file counts would drown the project's own.
var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, "target": true,
	"dist": true, "build": true, "out": true, ".venv": true, "venv": true,
	"__pycache__": true, ".next": true, ".tox": true, ".mypy_cache": true,
	".pytest_cache": true, ".gradle": true, ".idea": true, ".vscode": true,
}

// languages maps an extension to the name a person would use. An
// extension that is not here is not counted: the point is what the
// project is written in, not how many .gitignores it has.
var languages = map[string]string{
	".go": "Go", ".rs": "Rust", ".py": "Python", ".rb": "Ruby", ".java": "Java",
	".js": "JavaScript", ".mjs": "JavaScript", ".cjs": "JavaScript",
	".ts": "TypeScript", ".tsx": "TypeScript", ".jsx": "JavaScript",
	".c": "C", ".h": "C", ".cc": "C++", ".cpp": "C++", ".hpp": "C++",
	".cs": "C#", ".php": "PHP", ".swift": "Swift", ".kt": "Kotlin",
	".scala": "Scala", ".ex": "Elixir", ".exs": "Elixir", ".erl": "Erlang",
	".sh": "Shell", ".bash": "Shell", ".zsh": "Shell", ".lua": "Lua",
	".sql": "SQL", ".html": "HTML", ".css": "CSS", ".scss": "CSS",
	".md": "Markdown", ".proto": "Protocol Buffers", ".tf": "Terraform",
}

// Look surveys the repository. It reads a handful of declared files and
// counts the rest; it runs nothing.
func Look(cwd string) Survey {
	if abs, err := filepath.Abs(cwd); err == nil {
		cwd = abs
	}
	root := prompt.ProjectRoot(cwd)
	s := Survey{Root: root, Name: filepath.Base(root)}
	if p, ok := project.Detect(root); ok {
		s.Kind = p.Kind
	}
	byLang := map[string]int{}
	byDir := map[string]int{}
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || s.Files >= maxScan {
			if s.Files >= maxScan {
				return filepath.SkipAll
			}
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if p == root {
				return nil
			}
			if skipDirs[name] || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(name, ".") {
			return nil
		}
		s.Files++
		if lang, ok := languages[strings.ToLower(filepath.Ext(name))]; ok {
			byLang[lang]++
		}
		if rel, err := filepath.Rel(root, p); err == nil {
			if dir, _, ok := strings.Cut(filepath.ToSlash(rel), "/"); ok {
				byDir[dir]++
			}
		}
		return nil
	})
	s.Languages, s.Dirs = ranked(byLang, 6), ranked(byDir, 10)
	s.Commands = commands(root)
	s.Existing = existing(root)
	return s
}

// ranked turns a count map into the n biggest, ties broken by name so
// two runs of the same repository produce the same file.
func ranked(m map[string]int, n int) []Count {
	out := make([]Count, 0, len(m))
	for k, v := range m {
		out = append(out, Count{k, v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].N != out[j].N {
			return out[i].N > out[j].N
		}
		return out[i].Name < out[j].Name
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// commands collects what the repository declares it can do.
func commands(root string) []Command {
	var out []Command
	add := func(run, from string) {
		for _, c := range out {
			if c.Run == run {
				return
			}
		}
		out = append(out, Command{Run: run, From: from})
	}
	if vc, _, err := verify.Load(root); err == nil {
		for _, c := range vc.Commands {
			add(c, verify.ProjectFile+", run after every change")
		}
	}
	for _, t := range makeTargets(root) {
		add("make "+t, "Makefile")
	}
	for _, s := range npmScripts(root) {
		run := "npm run " + s
		if s == "test" || s == "start" {
			run = "npm " + s
		}
		add(run, "package.json")
	}
	has := func(name string) bool {
		_, err := os.Stat(filepath.Join(root, name))
		return err == nil
	}
	switch {
	case has("go.mod"):
		add("go build ./...", "go.mod")
		add("go test ./...", "go.mod")
	case has("Cargo.toml"):
		add("cargo build", "Cargo.toml")
		add("cargo test", "Cargo.toml")
	case has("pyproject.toml"), has("setup.py"), has("tox.ini"), has("requirements.txt"):
		add("pytest", "the Python project files")
	}
	if len(out) > 8 {
		out = out[:8]
	}
	return out
}

// makeTargets lists the conventional targets a Makefile declares, in the
// order a person would reach for them.
func makeTargets(root string) []string {
	var data []byte
	for _, name := range []string{"Makefile", "makefile", "GNUmakefile"} {
		if b, err := os.ReadFile(filepath.Join(root, name)); err == nil {
			data = b
			break
		}
	}
	if data == nil {
		return nil
	}
	declared := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" || line[0] == '\t' || line[0] == ' ' || line[0] == '#' || line[0] == '.' {
			continue
		}
		name, rest, ok := strings.Cut(line, ":")
		if !ok || strings.ContainsAny(name, "=$ ") || strings.HasPrefix(rest, "=") {
			continue
		}
		declared[strings.TrimSpace(name)] = true
	}
	var out []string
	// Four is as many as a page can carry; the rest are in the Makefile
	// for anyone who needs them.
	for _, want := range []string{"check", "test", "build", "lint", "fmt", "run", "install"} {
		if declared[want] && len(out) < 4 {
			out = append(out, want)
		}
	}
	return out
}

// npmScripts reads package.json's scripts, most useful first.
func npmScripts(root string) []string {
	data, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err != nil {
		return nil
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if json.Unmarshal(data, &pkg) != nil {
		return nil
	}
	var out []string
	for _, want := range []string{"test", "build", "lint", "typecheck", "dev", "start"} {
		if _, ok := pkg.Scripts[want]; ok {
			out = append(out, want)
		}
	}
	return out
}

// existing finds the instruction files that are already read, so the new
// one can say what they cover instead of saying it again.
func existing(root string) []Instruction {
	var out []Instruction
	for _, name := range prompt.InstructionFiles {
		if name == File {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			continue
		}
		in := Instruction{Name: name}
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "#") {
				h := strings.TrimSpace(strings.TrimLeft(line, "# "))
				if h != "" && len(in.Headings) < 8 {
					in.Headings = append(in.Headings, h)
				}
			}
		}
		out = append(out, in)
	}
	return out
}

// Generate renders the starter file. It is deliberately short, and every
// section it cannot fill from the repository becomes a question rather
// than a paragraph of filler.
func (s Survey) Generate() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", s.Name)
	b.WriteString("Notes for whoever works here next, human or model. janetcode reads\n")
	b.WriteString("this file at the start of every session in this directory. It was\n")
	b.WriteString("written from what the repository shows; correct anything it got wrong,\n")
	b.WriteString("and keep it to a page.\n")

	b.WriteString("\n## What this is\n\n")
	switch {
	case len(s.Languages) == 0 && s.Files == 0:
		b.WriteString("An empty directory. Say here what is about to be built and in what.\n")
	case len(s.Languages) == 0:
		fmt.Fprintf(&b, "%s, in no language this could name. Say here what they are.\n", textfmt.Plural(s.Files, "file"))
	default:
		var parts []string
		for _, l := range s.Languages {
			parts = append(parts, fmt.Sprintf("%d %s file%s", l.N, l.Name, map[bool]string{true: "", false: "s"}[l.N == 1]))
		}
		lead := "A project of"
		if s.Kind != "" {
			lead = "A " + s.Kind + " project:"
		}
		fmt.Fprintf(&b, "%s %s.\n", lead, strings.Join(parts, ", "))
		b.WriteString("\nSay here what it is *for* — the counts cannot.\n")
	}

	b.WriteString("\n## Commands\n\n")
	if len(s.Commands) == 0 {
		b.WriteString("None declared. If there is a build or a test command, write it here;\n")
		b.WriteString("`/verify set <cmd>` then runs it after every change.\n")
	} else {
		for _, c := range s.Commands {
			fmt.Fprintf(&b, "- `%s` — %s\n", c.Run, c.From)
		}
	}

	if len(s.Dirs) > 0 {
		b.WriteString("\n## Layout\n\n")
		for _, d := range s.Dirs {
			fmt.Fprintf(&b, "- `%s/` — %s\n", d.Name, textfmt.Plural(d.N, "file"))
		}
	}

	b.WriteString("\n## Conventions\n\n")
	b.WriteString("Nothing here was detected: this is the part only you know. What must\n")
	b.WriteString("pass before a commit, what not to touch, how things are named, which\n")
	b.WriteString("directory a new file belongs in.\n")

	if len(s.Existing) > 0 {
		b.WriteString("\n## Already written down\n\n")
		for _, in := range s.Existing {
			fmt.Fprintf(&b, "`%s` is in this repository and is read too", in.Name)
			if len(in.Headings) > 0 {
				fmt.Fprintf(&b, " — it covers %s", strings.Join(in.Headings, ", "))
			}
			b.WriteString(". Do not repeat it here.\n")
		}
	}
	return b.String()
}

// Generate surveys cwd and renders the file without writing anything.
func Generate(cwd string) string { return Look(cwd).Generate() }

// Run writes the file. It refuses to overwrite an existing one unless
// force is set: the file is the user's, and a generated page is a poor
// trade for a paragraph they wrote themselves.
func Run(cwd string, force bool) (cmdresult.Result, error) {
	s := Look(cwd)
	path := filepath.Join(s.Root, File)
	rel := File
	if r, err := filepath.Rel(cwd, path); err == nil && !strings.HasPrefix(r, "..") {
		rel = r
	}
	// Lstat, not Stat: a symlink is never written through, even with
	// force. A cloned repository can ship one dangling toward a file of
	// the user's that does not exist yet, and os.WriteFile would create
	// it there, with the repository's own verify commands in the body.
	fi, err := os.Lstat(path)
	if err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return cmdresult.Result{Error: rel + " is a symlink and was not touched; remove it to write a new one"}, nil
	}
	if err == nil && !force {
		res := cmdresult.Result{Error: rel + " already exists and was not touched"}
		res.Tips = append(res.Tips, "/init force overwrites it, or open it and add what is missing")
		if len(s.Existing) > 0 {
			res.Tips = append(res.Tips, "`"+s.Existing[0].Name+"` is read too, so anything already there does not need repeating")
		}
		return res, nil
	}
	body := s.Generate()
	flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if !force {
		// O_EXCL also refuses a symlink put there since the Lstat.
		flags = os.O_WRONLY | os.O_CREATE | os.O_EXCL
	}
	f, err := os.OpenFile(path, flags, 0o644)
	if err != nil {
		return cmdresult.Result{}, err
	}
	if _, err := f.WriteString(body); err != nil {
		f.Close()
		return cmdresult.Result{}, err
	}
	if err := f.Close(); err != nil {
		return cmdresult.Result{}, err
	}
	res := cmdresult.Result{Sheet: body, SheetTitle: rel, Plain: true}
	res.Notes = append(res.Notes, "wrote "+rel+", read at the start of every session here")
	res.Tips = append(res.Tips, "the Conventions section is the part only you can write")
	for _, in := range s.Existing {
		res.Tips = append(res.Tips, in.Name+" is read too; "+rel+" says so rather than repeating it")
	}
	return res, nil
}
