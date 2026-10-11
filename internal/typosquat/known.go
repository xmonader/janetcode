package typosquat

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/xmonader/janetcode/internal/depcheck"
)

// Ecosystems. Go, NPM and PyPI are depcheck's; PyImport is the name a
// Python import statement uses, which is not always the distribution's
// (import yaml installs PyYAML).
const (
	Go       = depcheck.Go
	NPM      = depcheck.NPM
	PyPI     = depcheck.PyPI
	PyImport = "python"
)

// Where a known name was found, best first: a finding names the project's
// own use of the real name over a list's.
const (
	SrcProject = "already used in this project"
	SrcLocal   = "a module of this repository"
	SrcCache   = "in the local module cache"
	SrcStdlib  = "the standard library"
	SrcPopular = "a well-known package"
)

var srcRank = map[string]int{SrcProject: 0, SrcLocal: 0, SrcStdlib: 1, SrcCache: 2, SrcPopular: 3}

// Known is the set of names an added one is compared with, per ecosystem,
// each with where it was found.
type Known struct {
	m map[string]map[string]string
}

// Add records name as known in eco. A name already known keeps its
// better source.
func (k *Known) Add(eco, name, src string) {
	if k.m == nil {
		k.m = map[string]map[string]string{}
	}
	if k.m[eco] == nil {
		k.m[eco] = map[string]string{}
	}
	name = normalize(eco, name)
	if name == "" {
		return
	}
	if old, ok := k.m[eco][name]; ok && srcRank[old] <= srcRank[src] {
		return
	}
	k.m[eco][name] = src
}

// Has reports whether name is known in eco.
func (k *Known) Has(eco, name string) bool {
	_, ok := k.m[eco][normalize(eco, name)]
	return ok
}

// normalize puts a name the way its ecosystem compares it: PyPI folds
// case and runs of - _ . (python_dateutil is python-dateutil, so it is
// not a squat of it); the others compare as written.
func normalize(eco, name string) string {
	name = strings.TrimSpace(name)
	if eco == PyPI {
		return strings.ToLower(pySep.ReplaceAllString(name, "-"))
	}
	return name
}

var pySep = regexp.MustCompile(`[-_.]+`)

// Popular is the well-known lists alone: what an empty project knows.
func Popular() *Known {
	k := &Known{}
	for eco, list := range map[string][]string{Go: popularGo, NPM: popularNPM, PyPI: popularPyPI, PyImport: popularPyImport} {
		for _, n := range list {
			k.Add(eco, n, SrcPopular)
		}
	}
	for _, n := range pyStdlib {
		k.Add(PyImport, n, SrcStdlib)
	}
	for _, n := range nodeBuiltins {
		k.Add(NPM, n, SrcStdlib)
	}
	return k
}

// Load is what this machine knows for a project rooted at root, for an
// edit to path: the well-known lists, the project's manifests, its
// node_modules, the Python modules beside the edited file and at the
// root, and the Go module cache.
func Load(root, path string) *Known {
	k := Popular()
	if root == "" {
		return k
	}
	if data, err := os.ReadFile(filepath.Join(root, "go.mod")); err == nil {
		if m := goModule.FindSubmatch(data); m != nil {
			k.Add(Go, string(m[1]), SrcProject)
		}
		for _, n := range depcheck.Parse(Go, data) {
			k.Add(Go, n, SrcProject)
		}
		for _, m := range goIndirect.FindAllSubmatch(data, -1) {
			k.Add(Go, string(m[1]), SrcProject)
		}
	}
	if data, err := os.ReadFile(filepath.Join(root, "package.json")); err == nil {
		for _, n := range depcheck.Parse(NPM, data) {
			k.Add(NPM, n, SrcProject)
		}
	}
	reqs, _ := filepath.Glob(filepath.Join(root, "requirements*.txt"))
	for _, r := range reqs {
		if data, err := os.ReadFile(r); err == nil {
			for _, n := range depcheck.Parse(PyPI, data) {
				k.Add(PyPI, n, SrcProject)
				k.Add(PyImport, strings.ReplaceAll(n, "-", "_"), SrcProject)
			}
		}
	}
	for _, n := range nodeModules(filepath.Join(root, "node_modules")) {
		k.Add(NPM, n, SrcProject)
	}
	dirs := []string{root, filepath.Join(root, "src")}
	if path != "" {
		dirs = append(dirs, filepath.Dir(path))
	}
	for _, d := range dirs {
		for _, n := range pyModules(d) {
			k.Add(PyImport, n, SrcLocal)
		}
	}
	for _, n := range goModCache() {
		k.Add(Go, n, SrcCache)
	}
	return k
}

var (
	goModule   = regexp.MustCompile(`(?m)^module\s+"?([^\s"]+)"?`)
	goIndirect = regexp.MustCompile(`(?m)^\s*(?:require\s+)?([^\s(]+\.[^\s(]+)\s+v[^\s]+\s+//\s*indirect`)
)

// nodeModules lists the packages installed in a node_modules directory,
// scoped ones as @scope/name.
func nodeModules(dir string) []string {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		n := e.Name()
		if strings.HasPrefix(n, ".") {
			continue
		}
		if strings.HasPrefix(n, "@") {
			sub, _ := os.ReadDir(filepath.Join(dir, n))
			for _, s := range sub {
				out = append(out, n+"/"+s.Name())
			}
			continue
		}
		out = append(out, n)
	}
	return out
}

// pyModules lists what a directory offers to a Python import: x.py and
// packages (directories).
func pyModules(dir string) []string {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		n := e.Name()
		switch {
		case strings.HasPrefix(n, "."):
		case e.IsDir():
			out = append(out, n)
		case strings.HasSuffix(n, ".py"):
			out = append(out, strings.TrimSuffix(n, ".py"))
		}
	}
	return out
}

// Bounds on the module-cache walk: a cache of a few thousand modules is
// read in well under the time; a larger one is read as far as it gets.
const (
	cacheMax    = 20000
	cacheBudget = 2 * time.Second
)

var modCache struct {
	once sync.Once
	list []string
}

// goModCache lists the module paths in the Go module cache's download
// directory, once per process.
func goModCache() []string {
	modCache.once.Do(func() {
		modCache.list = scanModCache(goModCacheDir(), cacheMax, cacheBudget)
	})
	return modCache.list
}

func goModCacheDir() string {
	if d := os.Getenv("GOMODCACHE"); d != "" {
		return d
	}
	if gp := os.Getenv("GOPATH"); gp != "" {
		return filepath.Join(filepath.SplitList(gp)[0], "pkg", "mod")
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, "go", "pkg", "mod")
	}
	return ""
}

// scanModCache walks <dir>/cache/download for the @v directories that
// mark a module, and unescapes their paths (!x is X).
func scanModCache(dir string, max int, budget time.Duration) []string {
	if dir == "" {
		return nil
	}
	base := filepath.Join(dir, "cache", "download")
	deadline := time.Now().Add(budget)
	var out []string
	_ = filepath.WalkDir(base, func(p string, d os.DirEntry, err error) error {
		if err != nil || len(out) >= max || time.Now().After(deadline) {
			return filepath.SkipAll
		}
		if !d.IsDir() {
			return nil
		}
		if d.Name() == "@v" {
			if rel, err := filepath.Rel(base, filepath.Dir(p)); err == nil {
				out = append(out, unescapeModule(filepath.ToSlash(rel)))
			}
			return filepath.SkipDir
		}
		if d.Name() == "sumdb" && filepath.Dir(p) == base {
			return filepath.SkipDir
		}
		return nil
	})
	return out
}

func unescapeModule(s string) string {
	var b strings.Builder
	up := false
	for _, r := range s {
		switch {
		case r == '!':
			up = true
		case up:
			b.WriteString(strings.ToUpper(string(r)))
			up = false
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
