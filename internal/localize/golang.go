package localize

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/xmonader/janetcode/internal/shell"
)

// Go is the `go test` runner. It names failing tests from `--- FAIL:`
// lines and the `FAIL\t<package>` line that closes each package, reruns
// just those with -run, and collects per-test coverage with
// -coverprofile, one test per run.
type Go struct{}

func (Go) Name() string { return "go test" }

var (
	// goFailRe is a top-level test's failure; subtests are indented and
	// their parent fails with them.
	goFailRe = regexp.MustCompile(`^--- FAIL: (\S+)`)
	// goPkgRe is a package's closing line: ok, FAIL or "?" (no test
	// files), the package, then a duration or a bracketed reason.
	goPkgRe = regexp.MustCompile(`^(ok|FAIL|\?)\s+(\S+)(?:\s+(.*))?$`)
	// goTestNameRe is a line `go test -list` prints for a test.
	goTestNameRe = regexp.MustCompile(`^Test\w*$`)
)

// goPkgStatus is what a package's closing line says.
type goPkgStatus struct {
	ran    bool // the tests ran (ok, FAIL with a duration, or none to run)
	failed bool
}

// parseGo reads a `go test` output: the failing top-level tests, each
// with the package whose closing line followed it, and how each package
// ended. Tests whose package line never came (the output was cut) are
// dropped: without it there is nothing to rerun them by.
func parseGo(output string) (failed []Test, pkgs map[string]goPkgStatus) {
	pkgs = map[string]goPkgStatus{}
	var pending []string
	seen := map[Test]bool{}
	sc := bufio.NewScanner(strings.NewReader(output))
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if m := goFailRe.FindStringSubmatch(line); m != nil {
			name, _, _ := strings.Cut(m[1], "/")
			pending = append(pending, name)
			continue
		}
		m := goPkgRe.FindStringSubmatch(line)
		if m == nil || m[2] == "command-line-arguments" {
			continue
		}
		rest := m[3]
		switch m[1] {
		case "ok":
			pkgs[m[2]] = goPkgStatus{ran: true}
		case "?":
			pkgs[m[2]] = goPkgStatus{ran: true}
		case "FAIL":
			ran := !strings.HasPrefix(rest, "[")
			pkgs[m[2]] = goPkgStatus{ran: ran, failed: true}
			for _, name := range pending {
				t := Test{Pkg: m[2], Name: name}
				if ran && !seen[t] {
					seen[t] = true
					failed = append(failed, t)
				}
			}
		}
		pending = nil
	}
	return failed, pkgs
}

// Failures names the failing tests in a `go test` output.
func (Go) Failures(output string) []Test {
	failed, _ := parseGo(output)
	return failed
}

// Run reruns tests (only those) in dir. The outcome is judged only when
// every package involved built and ran: a subset of hunks that does not
// compile says nothing about the tests.
func (Go) Run(ctx context.Context, dir string, tests []Test) Outcome {
	pkgs, names := splitTests(tests)
	args := append([]string{"test", "-count=1", "-run", runPattern(names)}, pkgs...)
	out, _ := goCmd(ctx, dir, args...)
	failed, status := parseGo(out)
	o := Outcome{Failed: map[Test]bool{}, Judged: ctx.Err() == nil, Output: out}
	for _, p := range pkgs {
		if !status[p].ran {
			o.Judged = false
		}
	}
	for _, t := range failed {
		o.Failed[t] = true
	}
	return o
}

// Spectra runs each test of the failing tests' packages alone with
// coverage: every failing test, and passing ones up to max runs in all.
// What it has when ctx ends is returned without an error: a spectrum
// from fewer tests ranks less sharply, it does not rank wrongly.
func (Go) Spectra(ctx context.Context, dir string, failing []Test, max int) ([]Spectrum, error) {
	pkgs, _ := splitTests(failing)
	isFailing := map[Test]bool{}
	for _, t := range failing {
		isFailing[t] = true
	}
	var plan []Test
	plan = append(plan, failing...)
	for _, p := range pkgs {
		out, err := goCmd(ctx, dir, "test", "-count=1", "-list", ".", p)
		if err != nil && ctx.Err() != nil {
			return nil, nil
		}
		for _, l := range strings.Split(out, "\n") {
			l = strings.TrimSpace(l)
			if t := (Test{Pkg: p, Name: l}); goTestNameRe.MatchString(l) && !isFailing[t] && len(plan) < max {
				plan = append(plan, t)
			}
		}
	}
	tmp, err := os.MkdirTemp("", "janetcode-sbfl-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	funcs := funcIndex{dir: dir, files: map[string][]Func{}}
	dirs := map[string]string{}
	var out []Spectrum
	for i, t := range plan {
		if ctx.Err() != nil {
			break
		}
		if _, ok := dirs[t.Pkg]; !ok {
			d, _ := goCmd(ctx, dir, "list", "-f", "{{.Dir}}", t.Pkg)
			dirs[t.Pkg] = strings.TrimSpace(d)
		}
		prof := filepath.Join(tmp, fmt.Sprintf("%d.out", i))
		res, _ := goCmd(ctx, dir, "test", "-count=1", "-run", runPattern([]string{t.Name}), "-coverprofile="+prof, t.Pkg)
		failed, status := parseGo(res)
		if ctx.Err() != nil || !status[t.Pkg].ran {
			continue
		}
		s := Spectrum{Test: t}
		for _, f := range failed {
			if f == t {
				s.Failed = true
			}
		}
		blocks, err := readProfile(prof)
		if err != nil {
			continue
		}
		s.Covered = funcs.covered(dirs[t.Pkg], blocks)
		out = append(out, s)
	}
	return out, nil
}

// splitTests is the distinct packages and test names, in first-seen order.
func splitTests(tests []Test) (pkgs, names []string) {
	seenP, seenN := map[string]bool{}, map[string]bool{}
	for _, t := range tests {
		if !seenP[t.Pkg] {
			seenP[t.Pkg] = true
			pkgs = append(pkgs, t.Pkg)
		}
		if !seenN[t.Name] {
			seenN[t.Name] = true
			names = append(names, t.Name)
		}
	}
	return pkgs, names
}

// runPattern is a -run expression matching exactly these top-level tests.
func runPattern(names []string) string {
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = regexp.QuoteMeta(n)
	}
	return "^(" + strings.Join(q, "|") + ")$"
}

// goCmd runs the go tool in dir, scrubbed of the agent's credentials as
// verify's commands are: it runs the tests the model just wrote.
func goCmd(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "go", args...)
	shell.Supervise(cmd)
	cmd.Dir = dir
	cmd.Env = append(shell.ScrubEnv(os.Environ()), "JANETCODE_VERIFY=1")
	out, err := cmd.CombinedOutput()
	if errors.Is(err, exec.ErrWaitDelay) {
		err = nil
	}
	return string(out), err
}

// block is one covered statement range of a coverage profile.
type block struct {
	file       string // the base name: a package's files share its directory
	start, end int
}

// readProfile reads the executed blocks of a -coverprofile file.
func readProfile(path string) ([]block, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []block
	for _, l := range strings.Split(string(data), "\n") {
		// name.go:12.34,15.2 3 1
		f := strings.Fields(l)
		if len(f) != 3 || f[2] == "0" {
			continue
		}
		file, rng, ok := strings.Cut(f[0], ":")
		if !ok {
			continue
		}
		a, b, ok := strings.Cut(rng, ",")
		if !ok {
			continue
		}
		start, err1 := strconv.Atoi(strings.SplitN(a, ".", 2)[0])
		end, err2 := strconv.Atoi(strings.SplitN(b, ".", 2)[0])
		if err1 != nil || err2 != nil {
			continue
		}
		out = append(out, block{file: filepath.Base(file), start: start, end: end})
	}
	return out, nil
}

// funcIndex maps a file's lines to the functions declared on them,
// parsing each file once.
type funcIndex struct {
	dir   string // the project root in the worktree
	files map[string][]Func
}

// covered is the functions the blocks of a package in pkgDir executed.
func (x funcIndex) covered(pkgDir string, blocks []block) []Func {
	seen := map[Func]bool{}
	var out []Func
	for _, b := range blocks {
		for _, f := range x.funcs(filepath.Join(pkgDir, b.file)) {
			if b.start >= f.Line && b.start <= f.End && !seen[f] {
				seen[f] = true
				out = append(out, f)
			}
		}
	}
	return out
}

func (x funcIndex) funcs(path string) []Func {
	if fs, ok := x.files[path]; ok {
		return fs
	}
	rel, err := filepath.Rel(x.dir, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		rel = path
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	var fs []Func
	if err == nil {
		for _, d := range file.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			fs = append(fs, Func{Path: filepath.ToSlash(rel), Name: funcName(fd), Line: fset.Position(fd.Pos()).Line, End: fset.Position(fd.End()).Line})
		}
	}
	x.files[path] = fs
	return fs
}

// funcName is a declaration's name as it reads in a stack trace:
// (*T).M, T.M or F.
func funcName(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return fd.Name.Name
	}
	t := fd.Recv.List[0].Type
	if ix, ok := t.(*ast.IndexExpr); ok {
		t = ix.X
	}
	if ix, ok := t.(*ast.IndexListExpr); ok {
		t = ix.X
	}
	switch r := t.(type) {
	case *ast.StarExpr:
		x := r.X
		if ix, ok := x.(*ast.IndexExpr); ok {
			x = ix.X
		}
		if ix, ok := x.(*ast.IndexListExpr); ok {
			x = ix.X
		}
		if id, ok := x.(*ast.Ident); ok {
			return "(*" + id.Name + ")." + fd.Name.Name
		}
	case *ast.Ident:
		return r.Name + "." + fd.Name.Name
	}
	return fd.Name.Name
}
