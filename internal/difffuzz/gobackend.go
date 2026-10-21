package difffuzz

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/shell"
)

// The Go backend. For each package the change touched it:
//
//  1. copies the module to a temporary directory;
//  2. writes the package as it was before the change beside it, as
//     <pkg>/janetcode_difffuzz_old — a subdirectory, so whatever internal/
//     packages the original may import, the copy may too;
//  3. finds the exported functions whose body changed, or that call
//     something in the package that did, and whose parameters testing.F
//     can generate (strings, []byte, numbers, bools) and whose results can
//     be compared by value;
//  4. writes one FuzzDiff_<Name> per function in <pkg>/janetcode_difffuzz_h,
//     calling old and new on each input, twice each — a function that
//     disagrees with itself is nondeterministic, and its input is skipped
//     rather than reported — seeded with the literals the package and its
//     tests already hold;
//  5. runs the seeds once, then fuzzes each function for its share of the
//     budget, stopping at the first disagreement.
//
// Methods, generic functions and parameters of the package's own types
// are not compared: building a receiver or a value of a named type needs
// knowledge of the type the AST alone does not give. They are where the
// backend grows next.

const (
	goOldDir     = "janetcode_difffuzz_old"
	goHarnessDir = "janetcode_difffuzz_h"
	// goCopyBudget bounds the module copy; a larger module is not fuzzed.
	goCopyBudget = 128 << 20
	// goBuildTimeout bounds the seed run, which includes the build.
	goBuildTimeout = 3 * time.Minute
	goMaxSeeds     = 24
	divergeMark    = "difffuzz-diverge: "
)

type goBackend struct{}

func (goBackend) Name() string { return "go" }

func (goBackend) Applies(files []File) bool {
	for _, f := range files {
		if goSource(f.Rel) {
			return true
		}
	}
	return false
}

func goSource(rel string) bool {
	return strings.HasSuffix(rel, ".go") && !strings.HasSuffix(rel, "_test.go")
}

// goPackage is one changed package, located in its module.
type goPackage struct {
	dir     string // slash path relative to Request.Root
	modRoot string // absolute module root
	modPath string // the module's path, from go.mod
	rel     string // the package's slash path relative to modRoot ("" at the root)
	files   []File // its changed files
}

func (p goPackage) importPath() string {
	if p.rel == "" {
		return p.modPath
	}
	return p.modPath + "/" + p.rel
}

func (goBackend) Run(ctx context.Context, req Request) Report {
	goBin, err := exec.LookPath("go")
	if err != nil {
		return Report{Skipped: "no go toolchain on PATH"}
	}
	pkgs, skipped := goPackages(req)
	if len(pkgs) == 0 {
		return Report{Skipped: skipped}
	}
	var rep Report
	deadline := time.Now().Add(req.Budget)
	for _, p := range pkgs {
		left := time.Until(deadline)
		if left <= 0 {
			break
		}
		r := runGoPackage(ctx, goBin, req, p, left)
		rep.Targets = append(rep.Targets, r.Targets...)
		rep.Inconclusive = append(rep.Inconclusive, r.Inconclusive...)
		if r.Output != "" {
			rep.Output = r.Output
		}
		if r.Diverged != nil {
			rep.Diverged = r.Diverged
			return rep
		}
		if r.Skipped != "" && len(pkgs) == 1 {
			rep.Skipped = r.Skipped
		}
		if ctx.Err() != nil {
			break
		}
	}
	if len(rep.Targets) == 0 && rep.Skipped == "" {
		rep.Skipped = "no changed exported function with parameters the fuzzer can generate"
	}
	return rep
}

// goPackages groups the changed Go files by package and finds each
// package's module. A package outside any module, or the module's main
// package, is left out.
func goPackages(req Request) ([]goPackage, string) {
	byDir := map[string]*goPackage{}
	var order []string
	why := "no changed Go source file"
	for _, f := range req.Files {
		if !goSource(f.Rel) {
			continue
		}
		dir := path.Dir(f.Rel)
		if p, ok := byDir[dir]; ok {
			p.files = append(p.files, f)
			continue
		}
		modRoot, modPath := findModule(filepath.Join(req.Root, filepath.FromSlash(dir)))
		if modRoot == "" {
			why = "the changed Go files are in no module (no go.mod)"
			continue
		}
		rel, err := filepath.Rel(modRoot, filepath.Join(req.Root, filepath.FromSlash(dir)))
		if err != nil {
			continue
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			rel = ""
		}
		byDir[dir] = &goPackage{dir: dir, modRoot: modRoot, modPath: modPath, rel: rel, files: []File{f}}
		order = append(order, dir)
	}
	var out []goPackage
	for _, d := range order {
		out = append(out, *byDir[d])
	}
	return out, why
}

var moduleRe = regexp.MustCompile(`(?m)^module\s+"?([^\s"]+)"?`)

// findModule walks up from dir to the go.mod that holds it.
func findModule(dir string) (root, modPath string) {
	for d := dir; ; d = filepath.Dir(d) {
		if data, err := os.ReadFile(filepath.Join(d, "go.mod")); err == nil {
			if m := moduleRe.FindSubmatch(data); m != nil {
				return d, string(m[1])
			}
			return "", ""
		}
		if filepath.Dir(d) == d {
			return "", ""
		}
	}
}

// runGoPackage compares one package within the time left.
func runGoPackage(ctx context.Context, goBin string, req Request, p goPackage, left time.Duration) Report {
	tmp, err := os.MkdirTemp("", "janetcode-difffuzz-*")
	if err != nil {
		return Report{Skipped: "no scratch directory: " + err.Error()}
	}
	defer os.RemoveAll(tmp)
	copyRoot := filepath.Join(tmp, "m")
	if err := copyModule(p.modRoot, copyRoot, goCopyBudget); err != nil {
		return Report{Skipped: "could not copy the module: " + err.Error()}
	}
	pkgDir := filepath.Join(copyRoot, filepath.FromSlash(p.rel))
	oldSrc, err := writeOldPackage(pkgDir, p.files)
	if err != nil {
		return Report{Skipped: "could not write the package as it was: " + err.Error()}
	}
	newSrc, err := goSources(pkgDir)
	if err != nil {
		return Report{Skipped: err.Error()}
	}
	an, err := analyse(oldSrc, newSrc)
	if err != nil {
		return Report{Skipped: err.Error()}
	}
	if an.pkgName == "main" {
		return Report{Skipped: "the changed package is a main package, which cannot be imported"}
	}
	targets := an.targets
	if len(targets) == 0 {
		return Report{Skipped: "no changed exported function in " + p.importPath() + " with parameters the fuzzer can generate" + an.skippedNote()}
	}
	if len(targets) > req.MaxTargets {
		targets = targets[:req.MaxTargets]
	}
	seeds := literalSeeds(oldSrc, testSources(pkgDir))
	harness := genHarness(p.importPath(), targets, seeds)
	hDir := filepath.Join(pkgDir, goHarnessDir)
	if err := os.MkdirAll(hDir, 0o755); err != nil {
		return Report{Skipped: err.Error()}
	}
	if err := os.WriteFile(filepath.Join(hDir, "difffuzz_test.go"), harness, 0o644); err != nil {
		return Report{Skipped: err.Error()}
	}
	pkgArg := "./" + path.Join(p.rel, goHarnessDir)
	env := append(os.Environ(), "GOPROXY=off", "GOTOOLCHAIN=local", "GOWORK=off")
	env = append(env, req.Env...)
	run := func(timeout time.Duration, args ...string) (string, error) {
		c, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		cmd := exec.CommandContext(c, goBin, args...)
		// The code under comparison is the model's and runs here. A
		// timeout or cancel used to kill go alone: the test binary, its
		// fuzz workers and anything they started (a server) ran on
		// after the run was over. Supervise kills the whole group and
		// bounds the wait on the pipe.
		shell.Supervise(cmd)
		cmd.Dir, cmd.Env = copyRoot, env
		out, err := cmd.CombinedOutput()
		// go test exited 0 and only the leaked child held the pipe:
		// the run itself passed.
		if errors.Is(err, exec.ErrWaitDelay) {
			err = nil
		}
		return strings.ReplaceAll(string(out), copyRoot, p.modRoot), err
	}
	rep := Report{}
	for _, t := range targets {
		rep.Targets = append(rep.Targets, Target{Package: p.importPath(), Func: t.name, Why: t.why})
	}
	// The seeds first: one build, every function, every seed input.
	out, err := run(goBuildTimeout, "test", "-count=1", "-run", "^FuzzDiff_", pkgArg)
	if d := parseDivergence(out); d != nil {
		rep.Diverged = d
		return rep
	}
	if err != nil {
		if ctx.Err() != nil {
			return Report{Skipped: "interrupted"}
		}
		return Report{Skipped: "the old and new package could not be built side by side", Output: tail(out, 1500)}
	}
	per := left / time.Duration(len(targets))
	if per < 2*time.Second {
		per = 2 * time.Second
	}
	for i, t := range targets {
		if ctx.Err() != nil {
			break
		}
		out, err := run(per+goBuildTimeout, "test", "-count=1", "-run", "^$", "-fuzz", "^FuzzDiff_"+t.name+"$",
			"-fuzztime", fmt.Sprintf("%dms", per.Milliseconds()), "-parallel", "2", pkgArg)
		if d := parseDivergence(out); d != nil {
			rep.Diverged = d
			return rep
		}
		if err != nil {
			rep.Inconclusive = append(rep.Inconclusive, t.name)
			rep.Output = tail(out, 1500)
			continue
		}
		rep.Targets[i].Passed = true
	}
	return rep
}

func parseDivergence(out string) *Divergence {
	i := strings.Index(out, divergeMark)
	if i < 0 {
		return nil
	}
	line := out[i+len(divergeMark):]
	if j := strings.IndexByte(line, '\n'); j >= 0 {
		line = line[:j]
	}
	var d Divergence
	if json.Unmarshal([]byte(strings.TrimSpace(line)), &d) != nil {
		return nil
	}
	return &d
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}

// copyModule copies a module for a throwaway build: without .git and
// other hidden directories, node_modules, or testdata (nothing the build
// of a package needs). It fails past budget bytes.
func copyModule(src, dst string, budget int64) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			if p != src && (strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules" || d.Name() == "testdata") {
				return filepath.SkipDir
			}
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if budget -= info.Size(); budget < 0 {
			return errors.New("the module is larger than the copy budget")
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}

// writeOldPackage writes the package as it was before the change into
// pkgDir/janetcode_difffuzz_old: the package's files as they are, with the
// changed ones put back, the created ones left out and the deleted ones
// restored. It returns the old Go sources by file name.
func writeOldPackage(pkgDir string, changed []File) (map[string][]byte, error) {
	oldDir := filepath.Join(pkgDir, goOldDir)
	if err := os.MkdirAll(oldDir, 0o755); err != nil {
		return nil, err
	}
	byName := map[string]File{}
	for _, f := range changed {
		byName[path.Base(f.Rel)] = f
	}
	entries, err := os.ReadDir(pkgDir)
	if err != nil {
		return nil, err
	}
	src := map[string][]byte{}
	for _, e := range entries {
		name := e.Name()
		if !e.Type().IsRegular() || strings.HasSuffix(name, "_test.go") || name == "go.mod" || name == "go.sum" || name == "go.work" {
			continue // a go.mod would make the copy a module of its own
		}
		data, err := os.ReadFile(filepath.Join(pkgDir, name))
		if err != nil {
			return nil, err
		}
		if f, ok := byName[name]; ok {
			if !f.Existed {
				continue
			}
			data = f.Before
		}
		if err := os.WriteFile(filepath.Join(oldDir, name), data, 0o644); err != nil {
			return nil, err
		}
		if strings.HasSuffix(name, ".go") {
			src[name] = data
		}
	}
	for name, f := range byName {
		if f.Existed && !f.Exists && goSource(name) {
			if err := os.WriteFile(filepath.Join(oldDir, name), f.Before, 0o644); err != nil {
				return nil, err
			}
			src[name] = f.Before
		}
	}
	return src, nil
}

// goSources reads the non-test Go files of a directory.
func goSources(dir string) (map[string][]byte, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	src := map[string][]byte{}
	for _, e := range entries {
		if e.Type().IsRegular() && goSource(e.Name()) {
			data, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				return nil, err
			}
			src[e.Name()] = data
		}
	}
	return src, nil
}

// testSources reads the package's test files: the inputs a test already
// uses are the best seeds there are.
func testSources(dir string) map[string][]byte {
	src := map[string][]byte{}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Type().IsRegular() && strings.HasSuffix(e.Name(), "_test.go") {
			if data, err := os.ReadFile(filepath.Join(dir, e.Name())); err == nil {
				src[e.Name()] = data
			}
		}
	}
	return src
}

// ---- analysis ----

// goTarget is one function to compare.
type goTarget struct {
	name    string
	params  []string // parameter types, one per parameter
	results int
	why     string
}

type analysis struct {
	pkgName string
	targets []goTarget
	// skipped counts changed exported functions left out, by reason.
	skipped map[string]int
}

func (a analysis) skippedNote() string {
	if len(a.skipped) == 0 {
		return ""
	}
	var parts []string
	for why, n := range a.skipped {
		parts = append(parts, fmt.Sprintf("%d %s", n, why))
	}
	sort.Strings(parts)
	return " (changed but not compared: " + strings.Join(parts, ", ") + ")"
}

// goDecls is one side of the package, parsed.
type goDecls struct {
	pkgName string
	funcs   map[string]*ast.FuncDecl // top-level functions by name; methods as Type.Method
	text    map[string]string        // each declared name's source, whitespace folded
}

func parseSide(src map[string][]byte) (goDecls, error) {
	fset := token.NewFileSet()
	d := goDecls{funcs: map[string]*ast.FuncDecl{}, text: map[string]string{}}
	names := make([]string, 0, len(src))
	for n := range src {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		data := src[name]
		f, err := parser.ParseFile(fset, name, data, parser.SkipObjectResolution)
		if err != nil {
			return d, fmt.Errorf("could not parse %s: %v", name, err)
		}
		if d.pkgName == "" {
			d.pkgName = f.Name.Name
		}
		text := func(n ast.Node) string {
			s, e := fset.Position(n.Pos()).Offset, fset.Position(n.End()).Offset
			return strings.Join(strings.Fields(string(data[s:e])), " ")
		}
		for _, decl := range f.Decls {
			switch x := decl.(type) {
			case *ast.FuncDecl:
				key := x.Name.Name
				if x.Recv != nil && len(x.Recv.List) > 0 {
					key = recvName(x.Recv.List[0].Type) + "." + key
				}
				d.funcs[key] = x
				d.text[key] = text(x)
			case *ast.GenDecl:
				for _, spec := range x.Specs {
					switch s := spec.(type) {
					case *ast.ValueSpec:
						for _, n := range s.Names {
							d.text[n.Name] = text(s)
						}
					case *ast.TypeSpec:
						d.text[s.Name.Name] = text(s)
					}
				}
			}
		}
	}
	return d, nil
}

func recvName(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.StarExpr:
		return recvName(t.X)
	case *ast.IndexExpr:
		return recvName(t.X)
	case *ast.IndexListExpr:
		return recvName(t.X)
	case *ast.Ident:
		return t.Name
	}
	return "?"
}

// analyse picks the functions to compare: exported, top-level, present on
// both sides with the same signature, changed themselves or calling
// (directly or through other functions of the package) something that
// changed, and callable with generated inputs.
func analyse(oldSrc, newSrc map[string][]byte) (analysis, error) {
	od, err := parseSide(oldSrc)
	if err != nil {
		return analysis{}, err
	}
	nd, err := parseSide(newSrc)
	if err != nil {
		return analysis{}, err
	}
	an := analysis{pkgName: nd.pkgName, skipped: map[string]int{}}
	// changed holds plain names: a method's change is seen by its name,
	// which over-approximates (two types' methods of one name) and only
	// costs fuzzing time.
	changed := map[string]string{}
	for key, t := range nd.text {
		if od.text[key] != t {
			changed[plain(key)] = "its body changed"
		}
	}
	for key := range od.text {
		if _, ok := nd.text[key]; !ok {
			changed[plain(key)] = "it was removed"
		}
	}
	affected := map[string]string{}
	for k, v := range changed {
		affected[k] = v
	}
	for grew := true; grew; {
		grew = false
		for key, fn := range nd.funcs {
			name := plain(key)
			if _, ok := affected[name]; ok || fn.Body == nil {
				continue
			}
			if ref := refersTo(fn.Body, affected); ref != "" {
				affected[name] = "it calls " + ref + ", which changed"
				grew = true
			}
		}
	}
	keys := make([]string, 0, len(nd.funcs))
	for k := range nd.funcs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var direct, indirect []goTarget
	for _, key := range keys {
		fn := nd.funcs[key]
		if fn.Recv != nil || !ast.IsExported(fn.Name.Name) {
			continue
		}
		why, ok := affected[fn.Name.Name]
		if !ok {
			continue
		}
		old, ok := od.funcs[key]
		switch {
		case !ok:
			an.skipped["new"]++
			continue
		case sigText(old.Type) != sigText(fn.Type):
			an.skipped["with a changed signature"]++
			continue
		case fn.Type.TypeParams != nil:
			an.skipped["generic"]++
			continue
		}
		t, reason := targetOf(fn, why)
		if reason != "" {
			an.skipped[reason]++
			continue
		}
		if why == "its body changed" {
			direct = append(direct, t)
		} else {
			indirect = append(indirect, t)
		}
	}
	an.targets = append(direct, indirect...)
	return an, nil
}

func plain(key string) string {
	if i := strings.LastIndexByte(key, '.'); i >= 0 {
		return key[i+1:]
	}
	return key
}

// refersTo is the first name in body that is in names.
func refersTo(body ast.Node, names map[string]string) string {
	found := ""
	ast.Inspect(body, func(n ast.Node) bool {
		if found != "" {
			return false
		}
		switch x := n.(type) {
		case *ast.Ident:
			if _, ok := names[x.Name]; ok {
				found = x.Name
			}
		case *ast.SelectorExpr:
			if _, ok := names[x.Sel.Name]; ok {
				found = x.Sel.Name
			}
		}
		return true
	})
	return found
}

func sigText(ft *ast.FuncType) string {
	var b strings.Builder
	exprList := func(fl *ast.FieldList) {
		if fl == nil {
			return
		}
		for _, f := range fl.List {
			n := max(len(f.Names), 1)
			for range n {
				b.WriteString(typeText(f.Type))
				b.WriteString(",")
			}
		}
	}
	exprList(ft.TypeParams)
	b.WriteString("(")
	exprList(ft.Params)
	b.WriteString(")(")
	exprList(ft.Results)
	b.WriteString(")")
	return b.String()
}

// typeText renders a type expression as Go source.
func typeText(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.ArrayType:
		if t.Len == nil {
			return "[]" + typeText(t.Elt)
		}
		if l, ok := t.Len.(*ast.BasicLit); ok {
			return "[" + l.Value + "]" + typeText(t.Elt)
		}
		return "[?]" + typeText(t.Elt)
	case *ast.MapType:
		return "map[" + typeText(t.Key) + "]" + typeText(t.Value)
	case *ast.StarExpr:
		return "*" + typeText(t.X)
	case *ast.SelectorExpr:
		return typeText(t.X) + "." + t.Sel.Name
	case *ast.Ellipsis:
		return "..." + typeText(t.Elt)
	case *ast.InterfaceType:
		if t.Methods == nil || len(t.Methods.List) == 0 {
			return "interface{}"
		}
	}
	return "?"
}

// fuzzable are the parameter types testing.F generates.
var fuzzable = map[string]bool{
	"string": true, "[]byte": true, "bool": true, "byte": true, "rune": true,
	"int": true, "int8": true, "int16": true, "int32": true, "int64": true,
	"uint": true, "uint8": true, "uint16": true, "uint32": true, "uint64": true,
	"float32": true, "float64": true,
}

// showable reports whether a result type compares by value when printed:
// a basic type, an error (compared by its message), or a slice, array or
// map of those. Pointers, functions, channels and the package's own types
// are not: their printed form is an address or depends on the package.
func showable(e ast.Expr, top bool) bool {
	switch t := e.(type) {
	case *ast.Ident:
		return fuzzable[t.Name] || t.Name == "error" && top
	case *ast.ArrayType:
		return showable(t.Elt, false)
	case *ast.MapType:
		return showable(t.Key, false) && showable(t.Value, false)
	}
	return false
}

func targetOf(fn *ast.FuncDecl, why string) (goTarget, string) {
	t := goTarget{name: fn.Name.Name, why: why}
	for _, f := range fn.Type.Params.List {
		ty := typeText(f.Type)
		if strings.HasPrefix(ty, "...") {
			return t, "variadic"
		}
		if !fuzzable[ty] {
			return t, "with parameters the fuzzer cannot generate"
		}
		for range max(len(f.Names), 1) {
			t.params = append(t.params, ty)
		}
	}
	if len(t.params) == 0 {
		return t, "without parameters"
	}
	if fn.Type.Results == nil || len(fn.Type.Results.List) == 0 {
		return t, "without results"
	}
	for _, f := range fn.Type.Results.List {
		if !showable(f.Type, true) {
			return t, "with results that do not compare by value"
		}
		t.results += max(len(f.Names), 1)
	}
	return t, ""
}

// ---- seeds ----

// seedPool is the literals of each kind the package and its tests hold,
// as Go source.
type seedPool struct {
	strs, ints, floats []string
}

// literalSeeds gathers the package's literals, its tests' first: the
// inputs a test already uses reach the branches the code has.
func literalSeeds(srcs ...map[string][]byte) seedPool {
	var p seedPool
	seen := map[string]bool{}
	add := func(list *[]string, v string) {
		if !seen[v] && len(*list) < goMaxSeeds {
			seen[v] = true
			*list = append(*list, v)
		}
	}
	for i := len(srcs) - 1; i >= 0; i-- {
		names := make([]string, 0, len(srcs[i]))
		for n := range srcs[i] {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			f, err := parser.ParseFile(token.NewFileSet(), n, srcs[i][n], parser.SkipObjectResolution)
			if err != nil {
				continue
			}
			ast.Inspect(f, func(node ast.Node) bool {
				if _, ok := node.(*ast.ImportSpec); ok {
					return false
				}
				lit, ok := node.(*ast.BasicLit)
				if !ok {
					return true
				}
				switch lit.Kind {
				case token.STRING:
					if s, err := strconv.Unquote(lit.Value); err == nil && len(s) <= 200 {
						add(&p.strs, strconv.Quote(s))
					}
				case token.INT:
					if v, err := strconv.ParseInt(lit.Value, 0, 64); err == nil {
						add(&p.ints, strconv.FormatInt(v, 10))
					}
				case token.CHAR:
					if s, err := strconv.Unquote(lit.Value); err == nil {
						for _, r := range s {
							add(&p.ints, strconv.Itoa(int(r)))
						}
					}
				case token.FLOAT:
					add(&p.floats, lit.Value)
				}
				return true
			})
		}
	}
	return p
}

// fits reports whether an integer literal is a value of type ty.
func fits(lit, ty string) bool {
	v, err := strconv.ParseInt(lit, 10, 64)
	if err != nil {
		return false
	}
	switch ty {
	case "int8":
		return v >= -128 && v <= 127
	case "int16":
		return v >= -1<<15 && v < 1<<15
	case "int32", "rune":
		return v >= -1<<31 && v < 1<<31
	case "uint8", "byte":
		return v >= 0 && v <= 255
	case "uint16":
		return v >= 0 && v < 1<<16
	case "uint32":
		return v >= 0 && v < 1<<32
	case "uint", "uint64":
		return v >= 0
	}
	return true
}

// seedTuples builds the f.Add argument lists for one function: the zero
// values, then the pool's literals spread across the parameters.
func seedTuples(params []string, pool seedPool) []string {
	pick := func(ty string, i int) string {
		var list []string
		switch ty {
		case "string", "[]byte":
			list = pool.strs
		case "float32", "float64":
			list = append(append([]string(nil), pool.floats...), pool.ints...)
		case "bool":
			return strconv.FormatBool(i%2 == 1)
		default:
			for _, l := range pool.ints {
				if fits(l, ty) {
					list = append(list, l)
				}
			}
		}
		lit := ""
		if len(list) > 0 && i > 0 {
			lit = list[(i-1)%len(list)]
		}
		switch {
		case ty == "[]byte" && lit == "":
			return "[]byte(nil)"
		case ty == "[]byte":
			return "[]byte(" + lit + ")"
		case lit == "" && ty == "string":
			return `""`
		case lit == "":
			return ty + "(0)"
		case ty == "string":
			return lit
		}
		return ty + "(" + lit + ")"
	}
	n := 1
	for _, ty := range params {
		switch ty {
		case "string", "[]byte":
			n = max(n, len(pool.strs)+1)
		case "bool":
			n = max(n, 2)
		default:
			n = max(n, len(pool.ints)+1)
		}
	}
	n = min(n, goMaxSeeds)
	seen := map[string]bool{}
	var out []string
	for i := range n {
		var args []string
		for j, ty := range params {
			k := i
			if i > 0 {
				k = (i+j-1)%max(n-1, 1) + 1 // spread: the second parameter starts one literal on
			}
			args = append(args, pick(ty, k))
		}
		s := strings.Join(args, ", ")
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// ---- harness ----

// genHarness writes the fuzz test comparing old and new.
func genHarness(importPath string, targets []goTarget, pool seedPool) []byte {
	var b bytes.Buffer
	b.WriteString("// Code generated by janetcode difffuzz. DO NOT EDIT.\n\n")
	b.WriteString("package " + goHarnessDir + "\n\n")
	b.WriteString("import (\n\t\"encoding/json\"\n\t\"fmt\"\n\t\"strings\"\n\t\"testing\"\n\n")
	fmt.Fprintf(&b, "\tnewpkg %q\n\toldpkg %q\n)\n\n", importPath, importPath+"/"+goOldDir)
	b.WriteString(harnessHelpers)
	for _, t := range targets {
		var params, args, fmts, vals []string
		for i, ty := range t.params {
			a := fmt.Sprintf("a%d", i)
			params = append(params, a+" "+ty)
			if ty == "[]byte" {
				// A function may write to its input: each call gets its own.
				args = append(args, "append([]byte(nil), "+a+"...)")
			} else {
				args = append(args, a)
			}
			fmts = append(fmts, "%#v")
			vals = append(vals, a)
		}
		var rs []string
		for i := range t.results {
			rs = append(rs, fmt.Sprintf("r%d", i))
		}
		call := func(pkg string) string {
			return fmt.Sprintf("func() []any { %s := %s.%s(%s); return []any{%s} }",
				strings.Join(rs, ", "), pkg, t.name, strings.Join(args, ", "), strings.Join(rs, ", "))
		}
		fmt.Fprintf(&b, "func FuzzDiff_%s(f *testing.F) {\n", t.name)
		for _, s := range seedTuples(t.params, pool) {
			fmt.Fprintf(&b, "\tf.Add(%s)\n", s)
		}
		fmt.Fprintf(&b, "\tf.Fuzz(func(t *testing.T, %s) {\n", strings.Join(params, ", "))
		fmt.Fprintf(&b, "\t\tdifffuzzCompare(t, %q, fmt.Sprintf(\"%s(%s)\", %s), %s, %s)\n",
			t.name, t.name, strings.Join(fmts, ", "), strings.Join(vals, ", "), call("oldpkg"), call("newpkg"))
		b.WriteString("\t})\n}\n\n")
	}
	return b.Bytes()
}

// harnessHelpers are the generated test's shared functions: a call
// rendered as text (a panic as "panic: ..."), an error by its message,
// and the comparison, which skips an input either side disagrees with
// itself on.
const harnessHelpers = `func difffuzzShow(v any) string {
	if err, ok := v.(error); ok {
		return fmt.Sprintf("error(%q)", err.Error())
	}
	return fmt.Sprintf("%#v", v)
}

func difffuzzCall(call func() []any) (out string) {
	defer func() {
		if r := recover(); r != nil {
			out = fmt.Sprintf("panic: %v", r)
		}
	}()
	var parts []string
	for _, v := range call() {
		parts = append(parts, difffuzzShow(v))
	}
	return strings.Join(parts, ", ")
}

func difffuzzSame(a, b string) bool {
	return a == b || strings.HasPrefix(a, "panic: ") && strings.HasPrefix(b, "panic: ")
}

func difffuzzCompare(t *testing.T, name, input string, old, cur func() []any) {
	o1, o2 := difffuzzCall(old), difffuzzCall(old)
	n1, n2 := difffuzzCall(cur), difffuzzCall(cur)
	if !difffuzzSame(o1, o2) || !difffuzzSame(n1, n2) {
		t.Skip("nondeterministic on this input")
	}
	if difffuzzSame(o1, n1) {
		return
	}
	line, _ := json.Marshal(map[string]string{"func": name, "input": input, "old": o1, "new": n1})
	t.Fatalf("` + divergeMark + `%s", line)
}

`
