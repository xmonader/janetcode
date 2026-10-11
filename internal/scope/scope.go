// Package scope infers a task's expected blast radius — its scope
// contract — and measures how far a run's edits went past it.
//
// The contract is read off the task's own words, deterministically: the
// files and directories it names (and, one level down, the ones named by
// a document it points at, such as "Read BRIEF.md"), widened to their
// closure — the rest of a named file's package directory, the files that
// import it, and the tests that go with it. An edit inside the contract
// is the task; an edit outside it is scope creep, which is sometimes
// exactly right (a helper the fix needs) and sometimes the start of a
// run wandering off, so it is never blocked: it is reported, with the
// one-line reason the edit was made for (its provenance intent), and a
// headless run carries the share as a metric.
//
// Dependency manifests and lock files (go.mod, package.json, Cargo.lock,
// ...) are incidental: an edit to one is neither in scope nor creep, and
// is counted apart. A task that names no file at all has no contract
// (Contract.Empty); a run under it reports no creep figure rather than
// calling every edit creep.
package scope

import (
	"bufio"
	"bytes"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/xmonader/janetcode/internal/change"
	"github.com/xmonader/janetcode/internal/project"
)

const (
	// maxFiles bounds the tree walk; a tree past it is inferred from the
	// files seen so far, and Contract.Truncated says so.
	maxFiles = 6000
	// maxRead bounds the bytes read from one file scanned for imports or
	// for a referenced document's mentions.
	maxRead = 128 << 10
	// maxBasename is how many files a bare basename may name before it is
	// too common to mean any one of them ("main.go" in a monorepo).
	maxBasename = 3
)

// Contract is the inferred scope of one task.
type Contract struct {
	// Mentioned is what the task names, repo-relative with forward
	// slashes: existing files, existing directories (with a trailing
	// slash), and files that do not exist yet (to be created).
	Mentioned []string `json:"mentioned"`
	// Files is every path the contract covers, Mentioned's closure.
	Files map[string]string `json:"-"`
	// Dirs are directories the contract covers whole: a named directory,
	// and the directory of a named file, so a new file beside it is in
	// scope too.
	Dirs []string `json:"dirs,omitempty"`
	// Truncated says the tree walk stopped at maxFiles.
	Truncated bool `json:"truncated,omitempty"`
}

// Empty reports a task that named nothing the tree could resolve: no
// contract, so no creep can be measured against it.
func (c Contract) Empty() bool { return len(c.Mentioned) == 0 }

// Why says why rel is in scope ("mentioned", "same package as x",
// "imports x", "test of x", "under dir/"), or "" when it is not.
func (c Contract) Why(rel string) string {
	rel = clean(rel)
	if w, ok := c.Files[rel]; ok {
		return w
	}
	for _, d := range c.Dirs {
		if strings.HasPrefix(rel, d) {
			return "under " + d
		}
	}
	return ""
}

// Covers reports whether rel is in scope.
func (c Contract) Covers(rel string) bool { return c.Why(rel) != "" }

// Size is how many files the contract names or reaches, counting a named
// file that is still to be created.
func (c Contract) Size() int { return len(c.Files) }

// pathRe matches a path-like token: something with a slash or a file
// extension. Backticks and quotes around it are trimmed by the caller.
var pathRe = regexp.MustCompile(`[A-Za-z0-9_.\-]*(?:/[A-Za-z0-9_.\-]+)+/?|[A-Za-z0-9_\-]+(?:\.[A-Za-z0-9_\-]+)*\.[A-Za-z][A-Za-z0-9]{0,5}\b`)

// wordRe matches a bare identifier-shaped word, for naming a package
// directory without a slash ("the minire package").
var wordRe = regexp.MustCompile(`\b[A-Za-z_][A-Za-z0-9_\-]{3,}\b`)

// tree is the repository as the walk found it.
type tree struct {
	root  string
	files []string            // repo-relative, sorted
	have  map[string]bool     // files
	dirs  map[string]bool     // directories, with a trailing slash
	base  map[string][]string // basename -> files
}

func walk(root string, skip map[string]bool) (tree, bool) {
	t := tree{root: root, have: map[string]bool{}, dirs: map[string]bool{}, base: map[string][]string{}}
	truncated := false
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil || rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if skipDir(d.Name()) {
				return filepath.SkipDir
			}
			t.dirs[rel+"/"] = true
			return nil
		}
		if skip[rel] {
			return nil
		}
		if len(t.files) >= maxFiles {
			truncated = true
			return filepath.SkipAll
		}
		t.files = append(t.files, rel)
		t.have[rel] = true
		t.base[path.Base(rel)] = append(t.base[path.Base(rel)], rel)
		return nil
	})
	sort.Strings(t.files)
	return t, truncated
}

func skipDir(name string) bool {
	switch name {
	case ".git", "node_modules", "vendor", ".venv", "venv", "target", "dist", "build", "__pycache__", ".janetcode", ".pytest_cache", ".mypy_cache":
		return true
	}
	return false
}

// Infer builds the contract for task over the tree at root, as the tree
// stands now. created are repo-relative paths the run itself created so
// far, left out of the walk: a file the run made is not one the task
// named, nor an importer that widens the scope.
func Infer(root, task string, created ...string) Contract {
	skip := map[string]bool{}
	for _, p := range created {
		skip[clean(p)] = true
	}
	t, truncated := walk(root, skip)
	c := Contract{Files: map[string]string{}, Truncated: truncated}
	seen := map[string]bool{}
	var docs []string
	add := func(m string) {
		if m == "" || seen[m] {
			return
		}
		seen[m] = true
		c.Mentioned = append(c.Mentioned, m)
		if isDoc(m) && t.have[m] {
			docs = append(docs, m)
		}
	}
	for _, m := range t.mentions(task) {
		add(m)
	}
	// One level down: a task that says "read BRIEF.md" puts its scope in
	// the brief. A document the brief names is not followed further.
	for _, d := range docs {
		data, err := readSmall(filepath.Join(root, filepath.FromSlash(d)))
		if err != nil {
			continue
		}
		for _, m := range t.mentions(string(data)) {
			add(m)
		}
	}
	sort.Strings(c.Mentioned)
	c.close(t)
	return c
}

// mentions resolves the path-like tokens and package-shaped words of
// text against the tree.
func (t tree) mentions(text string) []string {
	var out []string
	for _, tok := range pathRe.FindAllString(text, -1) {
		tok = strings.Trim(tok, "`'\".,:;()[]{}<>")
		tok = strings.TrimPrefix(tok, "./")
		if tok == "" || strings.Contains(tok, "://") || strings.HasPrefix(tok, "/") || strings.Contains(tok, "..") {
			continue
		}
		switch {
		case t.have[tok]:
			out = append(out, tok)
		case t.dirs[strings.TrimSuffix(tok, "/")+"/"]:
			out = append(out, strings.TrimSuffix(tok, "/")+"/")
		case !strings.Contains(tok, "/"):
			// A bare basename: every file of that name, unless it is
			// too common to mean any one of them; a source file of that
			// name nowhere in the tree is one to create at the top.
			if fs := t.base[tok]; len(fs) > 0 && len(fs) <= maxBasename {
				out = append(out, fs...)
			} else if len(fs) == 0 && looksLikeFile(tok) {
				out = append(out, tok)
			}
		case looksLikeFile(tok) && t.dirs[path.Dir(tok)+"/"]:
			// A file to be created in a directory that exists.
			out = append(out, tok)
		case looksLikeFile(tok) && !strings.Contains(path.Dir(tok), "/"):
			// A file to be created in a new top-level directory.
			out = append(out, tok)
		}
	}
	for _, w := range wordRe.FindAllString(text, -1) {
		if d := w + "/"; t.dirs[d] && !genericDir[strings.ToLower(w)] {
			out = append(out, d)
		}
	}
	return out
}

// genericDir are directory names too generic for a bare word to name.
var genericDir = map[string]bool{"src": true, "test": true, "tests": true, "docs": true, "internal": true, "pkg": true, "lib": true, "cmd": true, "spec": true, "examples": true, "scripts": true}

// looksLikeFile reports a token naming a file that may not exist yet: a
// known source, doc or config extension. Anything else shaped like
// a.b ("os.path", "json.dumps", "v1.2") is not taken for a path, or a
// task naming no file would get a contract every edit falls outside.
func looksLikeFile(tok string) bool {
	return !strings.HasSuffix(tok, "/") && (sourceExt[path.Ext(tok)] || isDoc(tok) || configExt[path.Ext(tok)])
}

var configExt = map[string]bool{".json": true, ".yaml": true, ".yml": true, ".toml": true, ".sh": true, ".sql": true, ".html": true, ".css": true, ".proto": true}

func isDoc(p string) bool {
	switch strings.ToLower(path.Ext(p)) {
	case ".md", ".markdown", ".rst", ".txt", ".adoc":
		return true
	}
	return false
}

// close widens the mentions to their closure: the rest of a named file's
// directory, a named directory whole, the files importing a named file's
// module, and tests whose name carries a named file's stem.
func (c *Contract) close(t tree) {
	mark := func(p, why string) {
		if _, ok := c.Files[p]; !ok {
			c.Files[p] = why
		}
	}
	dirs := map[string]bool{}
	var modules []string // stems and package names whose importers are in scope
	var stems []string
	for _, m := range c.Mentioned {
		if strings.HasSuffix(m, "/") {
			dirs[m] = true
			modules = append(modules, path.Base(strings.TrimSuffix(m, "/")))
			continue
		}
		mark(m, "mentioned")
		if isDoc(m) {
			// A brief or a README is read, not a package: its
			// directory (often the root) is not thereby in scope.
			continue
		}
		if d := path.Dir(m); d != "." {
			dirs[d+"/"] = true
			modules = append(modules, path.Base(d))
		}
		stem := strings.TrimSuffix(path.Base(m), path.Ext(m))
		if stem != "" && stem != "__init__" && stem != "index" && stem != "main" && stem != "mod" && stem != "lib" {
			modules = append(modules, stem)
			stems = append(stems, stem)
		}
	}
	for d := range dirs {
		c.Dirs = append(c.Dirs, d)
	}
	sort.Strings(c.Dirs)
	for _, f := range t.files {
		for _, d := range c.Dirs {
			if strings.HasPrefix(f, d) {
				mark(f, "under "+d)
			}
		}
	}
	for _, f := range t.files {
		if !project.IsTestFile(f) {
			continue
		}
		low := strings.ToLower(path.Base(f))
		for _, s := range stems {
			if len(s) >= 3 && strings.Contains(low, strings.ToLower(s)) {
				mark(f, "test of "+s)
				break
			}
		}
	}
	if len(modules) == 0 {
		return
	}
	modules = uniq(modules)
	for _, f := range t.files {
		if _, ok := c.Files[f]; ok || !sourceExt[path.Ext(f)] {
			continue
		}
		data, err := readSmall(filepath.Join(t.root, filepath.FromSlash(f)))
		if err != nil {
			continue
		}
		if m := importsAny(data, modules); m != "" {
			mark(f, "imports "+m)
		}
	}
}

// sourceExt are the file types whose import lines are scanned.
var sourceExt = map[string]bool{".go": true, ".py": true, ".ts": true, ".tsx": true, ".js": true, ".jsx": true, ".mjs": true, ".rs": true, ".java": true, ".kt": true, ".rb": true, ".c": true, ".h": true, ".cc": true, ".cpp": true, ".hpp": true, ".cs": true, ".swift": true}

// importLine matches the start of an import in the languages above; the
// module names are then looked for as whole path elements on it.
var importLine = regexp.MustCompile(`^\s*(import\b|from\s|use\s|mod\s|#include\b)|\brequire\(`)

// importsAny names the first of modules that one of data's import lines
// names as a whole path element, or "".
func importsAny(data []byte, modules []string) string {
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64<<10), maxRead)
	inGoBlock := false
	for sc.Scan() {
		line := sc.Text()
		trim := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trim, "import ("):
			inGoBlock = true
			continue
		case inGoBlock && trim == ")":
			inGoBlock = false
			continue
		}
		if !inGoBlock && !importLine.MatchString(line) {
			continue
		}
		elems := strings.FieldsFunc(trim, func(r rune) bool {
			return !(r == '_' || r == '-' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z')
		})
		for _, m := range modules {
			for _, e := range elems {
				if e == m {
					return m
				}
			}
		}
	}
	return ""
}

func readSmall(p string) ([]byte, error) {
	st, err := os.Stat(p)
	if err != nil {
		return nil, err
	}
	if st.Size() > maxRead {
		return nil, fs.ErrInvalid
	}
	return os.ReadFile(p)
}

func uniq(s []string) []string {
	sort.Strings(s)
	out := s[:0]
	for i, v := range s {
		if i == 0 || v != s[i-1] {
			out = append(out, v)
		}
	}
	return out
}

func clean(rel string) string {
	return strings.TrimPrefix(path.Clean(filepath.ToSlash(rel)), "./")
}

// Incidental reports a dependency manifest or lock file: an edit the
// toolchain asks for (go mod tidy, npm install) rather than a choice
// about what the task covers.
func Incidental(rel string) bool {
	switch path.Base(clean(rel)) {
	case "go.mod", "go.sum", "go.work", "go.work.sum",
		"package.json", "package-lock.json", "yarn.lock", "pnpm-lock.yaml", "bun.lockb",
		"Cargo.toml", "Cargo.lock", "pyproject.toml", "poetry.lock", "uv.lock", "Pipfile", "Pipfile.lock",
		"requirements.txt", "requirements-dev.txt", "Gemfile", "Gemfile.lock", ".gitignore":
		return true
	}
	return false
}

// Outside is one file a run changed outside its contract, with the
// reason the edit was made for — its provenance intent, the one line the
// model gave for the call — or Unjustified when it gave none.
type Outside struct {
	Path          string `json:"path"`
	Turn          int    `json:"turn"`
	Justification string `json:"justification,omitempty"`
	Unjustified   bool   `json:"unjustified,omitempty"`
}

// Report is a run's edits measured against its contract.
type Report struct {
	// Mentioned is what the task named (Contract.Mentioned) and Size how
	// many existing files the contract covers.
	Mentioned []string `json:"mentioned"`
	Size      int      `json:"scope_files"`
	// Edited is how many distinct files the run changed, Incidental how
	// many of those were manifests or lock files.
	Edited     int `json:"edited"`
	Incidental int `json:"incidental,omitempty"`
	// Outside lists the rest that fell outside the contract.
	Outside []Outside `json:"outside,omitempty"`
	// Creep is len(Outside) over the edited files that are not
	// incidental: 0 when every choice the run made was inside the
	// task's scope.
	Creep float64 `json:"creep"`
}

// Measure folds a run's changes (the provenance record) against c. root
// makes the changes' absolute paths relative; a change outside root, or
// one since undone, is not counted. Its justification is the intent of
// the first change that touched the path.
func Measure(c Contract, root string, changes []change.Change) *Report {
	if c.Empty() {
		return nil
	}
	r := &Report{Mentioned: c.Mentioned, Size: c.Size()}
	seen := map[string]bool{}
	scored := 0
	for _, ch := range changes {
		if ch.Undone {
			continue
		}
		rel := ch.Path
		if filepath.IsAbs(rel) {
			x, err := filepath.Rel(root, rel)
			if err != nil || strings.HasPrefix(x, "..") {
				continue
			}
			rel = x
		}
		rel = clean(rel)
		if seen[rel] {
			continue
		}
		seen[rel] = true
		r.Edited++
		if Incidental(rel) {
			r.Incidental++
			continue
		}
		scored++
		if c.Covers(rel) {
			continue
		}
		why := strings.TrimSpace(ch.Intent)
		r.Outside = append(r.Outside, Outside{Path: rel, Turn: ch.Turn, Justification: why, Unjustified: why == ""})
	}
	if scored > 0 {
		r.Creep = float64(len(r.Outside)) / float64(scored)
	}
	return r
}
