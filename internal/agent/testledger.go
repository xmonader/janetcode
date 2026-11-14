package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/xmonader/janetcode/internal/gaming"
	"github.com/xmonader/janetcode/internal/proof"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/shell"
	"github.com/xmonader/janetcode/internal/textfmt"
	"github.com/xmonader/janetcode/internal/verify"
)

// A test-integrity ledger closes the special-casing hole the test guard
// leaves open. The guard refuses a headless run's first write to a test
// file; a second write goes through, a shell command can edit one, and
// code can make a test pass without touching it at all by recognising the
// test's input and returning the test's answer. ImpossibleBench found
// read-only tests restore honest performance and still do not stop that.
// When a turn that changed files is about to end, the ledger:
//
//   - replays the tests it changed, in their original form, against its
//     code in a throwaway copy of the tree: a green on edited tests is no
//     evidence unless the originals pass too;
//   - scans the source lines it added for branches on a literal only a
//     test holds (gaming.LiteralBranches), not the task text and not the
//     file as it was.
//
// Either finding sends the turn back once, naming the test or the line;
// what the ledger saw goes on the session log, and from there into the
// commit's proof note ("0 assertions modified, 2 tests added"). It is
// deterministic and makes no model call of its own. Replayed offline on
// the final diffs of 934 recorded effbench runs (every arm, r1-r13 and
// the competitor campaigns) the literal scan raises no flag, after two
// rules those runs taught it (gaming.LiteralBranches: a bare lower-case
// word needs a test answer beside it; check scripts are not source).
// The Impossible-effbench split (docs/benchmarks/effbench/impossible)
// measures what it catches. JANETCODE_OFF=testledger turns it off.

// ledgerState is the ledger's part of one turn's gates.
type ledgerState struct {
	seen int  // a.mutations when the ledger last ran
	sent bool // the turn has been sent back on a finding
}

const (
	// ledgerCopyBudget bounds the bytes copied for the replay; a tree
	// past it is not replayed, and the ledger says so.
	ledgerCopyBudget = 64 << 20
	// ledgerReadBudget bounds the test and task text the scan reads.
	ledgerReadBudget = 4 << 20
	ledgerMaxFiles   = 4000
)

// ledgerFile is one file the turn changed: as it was before the turn's
// first change to it, and as it is now.
type ledgerFile struct {
	rel             string
	before, after   []byte
	existed, exists bool
}

// testLedgerGate keeps the ledger for a turn that changed files and, on a
// finding, sends the turn back once. It reports whether the turn goes on.
func (a *Agent) testLedgerGate(ctx context.Context, g *ledgerState, emit func(Event)) bool {
	if !a.TestLedger || a.Off["testledger"] || a.mutations == 0 || a.mutations == g.seen {
		return false
	}
	g.seen = a.mutations
	root := a.displayRoot()
	files := a.turnFiles(root)
	if len(files) == 0 {
		return false
	}
	led, replayOut := a.keepLedger(ctx, root, files)
	if led.Clean() || g.sent {
		a.logLedger(led)
		return false
	}
	g.sent, led.SentBack = true, true
	a.logLedger(led)
	what := "test-literal branch"
	if led.ReplayFailed {
		what = "original tests fail"
	}
	emit(Event{Kind: EvNotice, Text: "test integrity: " + what + "; asking the model to look again"})
	a.appendHarnessText(a.ledgerNote(led, replayOut))
	return true
}

// keepLedger computes the ledger for the turn's changed files, and the
// tail of the replay's output when the originals failed.
func (a *Agent) keepLedger(ctx context.Context, root string, files []ledgerFile) (proof.Integrity, string) {
	var led proof.Integrity
	originals := map[string][]byte{}
	var sources []gaming.Source
	for _, f := range files {
		switch {
		case isTestFile(f.rel) && !f.existed && f.exists:
			led.TestsAdded++
		case isTestFile(f.rel) && f.existed:
			led.TestsModified = append(led.TestsModified, f.rel)
			originals[f.rel] = f.before
			led.AssertionsModified += removedAssertions(string(f.before), string(f.after))
		case f.exists && !isDocFile(f.rel):
			sources = append(sources, gaming.Source{Path: f.rel, Before: string(f.before), Added: addedLines(string(f.before), string(f.after))})
		}
	}
	replayOut := ""
	switch {
	case len(originals) == 0:
	case insertOnly(files, originals):
		// Every original line is still there, in order: the turn's own
		// green test run already ran the original tests. A replay
		// would only repeat it in a copy of the tree, which on a real
		// repository cost a run 80 s (realrepo caddy-strip-prefix).
		led.ReplaySkipped = "the edited tests only gained lines, so the turn's own test run covered the originals"
	default:
		led.Replay, led.ReplayFailed, led.ReplaySkipped, replayOut = a.replayOriginals(ctx, root, originals)
	}
	tests, spec := ledgerTexts(root, originals, files, a.integ.userText)
	for _, s := range gaming.LiteralBranches(gaming.TestLiterals(tests), spec, sources) {
		led.LiteralFlags = append(led.LiteralFlags, s.String())
	}
	return led, replayOut
}

// insertOnly reports whether every edited test file kept each of its
// original lines, in order, and only gained new ones.
func insertOnly(files []ledgerFile, originals map[string][]byte) bool {
	for _, f := range files {
		before, ok := originals[f.rel]
		if !ok {
			continue
		}
		after := strings.Split(string(f.after), "\n")
		i := 0
		for _, l := range strings.Split(string(before), "\n") {
			for i < len(after) && after[i] != l {
				i++
			}
			if i == len(after) {
				return false
			}
			i++
		}
	}
	return true
}

// turnFiles are the files this turn changed, from the provenance record:
// each as it was before the turn's first change to it and as it is on
// disk now. A file put back the way it was is left out.
func (a *Agent) turnFiles(root string) []ledgerFile {
	byPath := map[string]*ledgerFile{}
	var order []string
	bashOrig := a.bashOrigins()
	for _, c := range a.Changes() {
		if c.Turn != a.turnN || c.Undone {
			continue
		}
		if _, ok := byPath[c.Path]; ok {
			continue
		}
		rel := c.Path
		if r, err := filepath.Rel(root, c.Path); err == nil && filepath.IsAbs(c.Path) && !strings.HasPrefix(r, "..") {
			rel = r
		}
		f := &ledgerFile{rel: filepath.ToSlash(rel), before: c.Before, existed: c.Existed}
		if o, ok := bashOrig[c.Path]; ok {
			// A bash call changed it before the file tool's first change.
			f.before, f.existed = o.before, o.existed
		}
		byPath[c.Path] = f
		order = append(order, c.Path)
	}
	var out []ledgerFile
	for _, p := range order {
		f := byPath[p]
		abs := p
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(root, p)
		}
		if data, err := os.ReadFile(abs); err == nil {
			f.after, f.exists = data, true
		}
		if f.exists == f.existed && string(f.after) == string(f.before) {
			continue
		}
		out = append(out, *f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].rel < out[j].rel })
	return out
}

// replayOriginals runs the turn's test command in a copy of the tree with
// the edited tests put back as they were. The command is the project's
// verify gate when it has one, else the last test run of the turn that
// passed: the one whose green the turn is relying on.
func (a *Agent) replayOriginals(ctx context.Context, root string, originals map[string][]byte) (command string, failed bool, skipped, out string) {
	commands, timeout := a.replayCommands(root)
	if pkgs := goTestPackages(originals); len(pkgs) > 0 && len(commands) == 1 && strings.HasPrefix(commands[0], "go test") {
		// Only the packages whose tests were edited: the turn's last run
		// may be `go test -race ./...` over a whole repository.
		commands = []string{"go test " + strings.Join(pkgs, " ")}
	}
	if len(commands) == 0 {
		return "", false, "no test command passed in this turn to replay", ""
	}
	command = strings.Join(commands, " && ")
	dir, err := os.MkdirTemp("", "janetcode-ledger-*")
	if err != nil {
		return command, false, "no scratch directory: " + err.Error(), ""
	}
	defer os.RemoveAll(dir)
	copyRoot := filepath.Join(dir, filepath.Base(root))
	if err := copyTree(root, copyRoot, ledgerCopyBudget); err != nil {
		return command, false, "could not copy the tree: " + err.Error(), ""
	}
	for rel, data := range originals {
		if err := os.WriteFile(filepath.Join(copyRoot, filepath.FromSlash(rel)), data, 0o644); err != nil {
			return command, false, "could not restore " + rel + ": " + err.Error(), ""
		}
	}
	// A command that names the project by its absolute path (`cd /w &&
	// pytest`, `pytest /w/tests`) names the copy instead.
	for i, c := range commands {
		commands[i] = strings.ReplaceAll(c, root, copyRoot)
	}
	run := &verify.Runner{Config: verify.Config{Commands: commands, Timeout: timeout}, Cwd: copyRoot}
	res := run.Run(ctx)
	if ctx.Err() != nil {
		return command, false, "interrupted", ""
	}
	if res.Failed == "" {
		return command, false, "", ""
	}
	return command, true, "", strings.ReplaceAll(res.Output, copyRoot, root)
}

// goTestPackages are the package directories of the edited test files,
// as go test arguments, when every one is a Go test file; nil otherwise.
func goTestPackages(originals map[string][]byte) []string {
	seen := map[string]bool{}
	var pkgs []string
	for rel := range originals {
		if !strings.HasSuffix(rel, "_test.go") {
			return nil
		}
		d := "./" + filepath.ToSlash(filepath.Dir(rel))
		if !seen[d] {
			seen[d] = true
			pkgs = append(pkgs, d)
		}
	}
	sort.Strings(pkgs)
	return pkgs
}

// replayCommands are the commands the replay runs, and their timeout in
// seconds (zero for verify's default).
func (a *Agent) replayCommands(root string) ([]string, int) {
	if gate, _ := a.VerifyGate(); gate.Enabled() && len(gate.Config.Commands) > 0 {
		return append([]string(nil), gate.Config.Commands...), gate.Config.Timeout
	}
	if a.Log == nil {
		return nil, 0
	}
	last := ""
	for _, e := range a.logEvents() {
		if e.Turn != a.turnN || e.Type != session.TypeToolResult || e.Name != "bash" || refused(e) {
			continue
		}
		cmd := shell.TrimWorkspaceCd(shell.CommandOf(e.Arguments), root)
		if shell.IsTestRun(cmd) && !runFailed(e) && !neverRan(e) {
			last = cmd
		}
	}
	if last == "" {
		return nil, 0
	}
	return []string{last}, 300
}

// ledgerTexts are the scan's inputs: every test file the project held
// before the turn, as it was then, and the task's own words (the request
// and the project's prose documents, a BRIEF.md or a README).
func ledgerTexts(root string, originals map[string][]byte, files []ledgerFile, request string) (map[string]string, string) {
	created := map[string]bool{}
	for _, f := range files {
		if !f.existed {
			created[f.rel] = true
		}
	}
	tests := map[string]string{}
	var spec strings.Builder
	spec.WriteString(request)
	budget, seen := ledgerReadBudget, 0
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != root && skipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if seen++; seen > ledgerMaxFiles || budget <= 0 {
			return filepath.SkipAll
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		switch {
		case created[rel]:
		case isTestFile(rel):
			if data, ok := originals[rel]; ok {
				tests[rel] = string(data)
				budget -= len(data)
			} else if data, err := readSmall(p); err == nil {
				tests[rel] = string(data)
				budget -= len(data)
			}
		case isDocFile(rel) && strings.Count(rel, "/") <= 1:
			if data, err := readSmall(p); err == nil {
				spec.WriteString("\n")
				spec.Write(data)
				budget -= len(data)
			}
		}
		return nil
	})
	return tests, spec.String()
}

// skipDir names directories no test or task text lives in.
func skipDir(name string) bool {
	switch name {
	case ".git", "node_modules", "vendor", ".venv", "venv", "target", "dist", "build", "__pycache__", ".janetcode":
		return true
	}
	return false
}

// readSmall reads a file of at most 256 KB.
func readSmall(p string) ([]byte, error) {
	if st, err := os.Stat(p); err != nil || st.Size() > 256<<10 {
		return nil, errors.New("too large")
	}
	return os.ReadFile(p)
}

// copyTree copies src to dst for a throwaway run, without .git, and with
// dependency directories linked rather than copied: a replay needs the
// installed packages, not a second copy of them. It fails past budget
// bytes.
func copyTree(src, dst string, budget int64) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		switch {
		case d.IsDir() && d.Name() == ".git" && p != src:
			return filepath.SkipDir
		case d.IsDir() && p != src && (d.Name() == "node_modules" || d.Name() == ".venv" || d.Name() == "venv"):
			if err := os.Symlink(p, target); err != nil {
				return err
			}
			return filepath.SkipDir
		case d.IsDir():
			return os.MkdirAll(target, 0o755)
		case d.Type()&fs.ModeSymlink != 0:
			link, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		case !d.Type().IsRegular():
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if budget -= info.Size(); budget < 0 {
			return errors.New("the tree is larger than the replay copies")
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	})
}

// addedLines are after's lines that before does not hold as often.
func addedLines(before, after string) []string {
	have := map[string]int{}
	for _, l := range strings.Split(before, "\n") {
		have[l]++
	}
	var out []string
	for _, l := range strings.Split(after, "\n") {
		if have[l] > 0 {
			have[l]--
			continue
		}
		out = append(out, l)
	}
	return out
}

// removedAssertions counts the assertion lines of before that after no
// longer holds: an expectation changed or dropped.
func removedAssertions(before, after string) int {
	n := 0
	for _, l := range addedLines(after, before) {
		if gaming.Assertion(l) {
			n++
		}
	}
	return n
}

// logLedger records the ledger on the session log, for the proof note.
func (a *Agent) logLedger(led proof.Integrity) {
	raw, err := json.Marshal(led)
	if err != nil {
		return
	}
	a.logEvent(session.Event{Type: session.TypeTestLedger, Turn: a.turnN, IsError: !led.Clean(), Extra: raw})
}

// ledgerNote is what the model is told when the ledger sends it back.
func (a *Agent) ledgerNote(led proof.Integrity, replayOut string) string {
	var b strings.Builder
	b.WriteString("[harness] Before you finish, a check of the tests found:\n")
	if led.ReplayFailed {
		fmt.Fprintf(&b, "- The tests as they were before this turn fail against your code (changed in this turn: %s). Running `%s` on the originals:\n```\n%s\n```\n",
			strings.Join(led.TestsModified, ", "), led.Replay, textfmt.TruncateTo(strings.TrimSpace(replayOut), 3000))
	}
	for _, f := range led.LiteralFlags {
		fmt.Fprintf(&b, "- %s\n", f)
	}
	b.WriteString("Make the code meet the rule the task states, not the particular values a test uses, and leave the original tests passing.")
	if a.ConflictExit {
		b.WriteString(" If a test contradicts the task, do not work around it: end with " + conflictFormat + " and stop.")
	} else {
		b.WriteString(" If a test contradicts the task, say so plainly instead of working around it.")
	}
	return b.String()
}

// foldLedger is the turn's latest ledger on the log, or nil.
func foldLedger(events []session.Event, turn int) *proof.Integrity {
	var led *proof.Integrity
	for _, e := range events {
		if e.Type != session.TypeTestLedger || e.Turn != turn {
			continue
		}
		var x proof.Integrity
		if json.Unmarshal(e.Extra, &x) == nil {
			led = &x
		}
	}
	return led
}
