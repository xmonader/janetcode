// Package mutate checks that a change is actually tested: it flips
// operators on the lines the agent changed, one at a time, and runs the
// test command against each mutant in a throwaway worktree. A mutant the
// tests do not catch means the behaviour on that line is unverified.
package mutate

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/gitx"
	"github.com/xmonader/janetcode/internal/project"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/shell"
	"github.com/xmonader/janetcode/internal/snapshot"
	"github.com/xmonader/janetcode/internal/textfmt"
	"github.com/xmonader/janetcode/internal/worktree"
)

// Mutant is one operator flip on one line.
type Mutant struct {
	Path   string // relative to the root
	Line   int    // 1-based
	From   string // operator replaced
	To     string
	Before string // the original line, trimmed
	After  string // the mutated line, trimmed
}

func (m Mutant) String() string {
	return fmt.Sprintf("%s:%d: `%s` → `%s`", m.Path, m.Line, m.From, m.To)
}

// operator is one swap; the pattern must not match inside longer tokens.
type operator struct {
	re       *regexp.Regexp
	from, to string
}

var operators = []operator{
	{regexp.MustCompile(`==`), "==", "!="},
	{regexp.MustCompile(`!=`), "!=", "=="},
	{regexp.MustCompile(`<=`), "<=", "<"},
	{regexp.MustCompile(`>=`), ">=", ">"},
	{regexp.MustCompile(`(^|[^<=!\-])<([^=<\-])`), "<", "<="},
	{regexp.MustCompile(`(^|[^>=\-])>([^=>])`), ">", ">="},
	{regexp.MustCompile(`&&`), "&&", "||"},
	{regexp.MustCompile(`\|\|`), "||", "&&"},
	{regexp.MustCompile(`\btrue\b`), "true", "false"},
	{regexp.MustCompile(`\bfalse\b`), "false", "true"},
	{regexp.MustCompile(`\bTrue\b`), "True", "False"},
	{regexp.MustCompile(`\bFalse\b`), "False", "True"},
	{regexp.MustCompile(`([\w)\]]) \+ ([\w(\[])`), "+", "-"},
	{regexp.MustCompile(`([\w)\]]) - ([\w(\[])`), "-", "+"},
}

// sourceExt lists the files worth mutating.
var sourceExt = map[string]bool{".go": true, ".py": true, ".ts": true, ".tsx": true, ".js": true, ".jsx": true, ".rs": true, ".c": true, ".cc": true, ".cpp": true, ".h": true, ".java": true, ".kt": true, ".rb": true, ".cs": true, ".swift": true, ".zig": true}

// equivalent reports whether flipping op.from to op.to at [start,end) of
// masked produces a mutant that means exactly what the original did. Such
// a mutant can never be killed, so generating one spends a full test run
// to learn nothing and then reports a survivor the model is asked to
// write a test for — a test that cannot exist.
//
// The case that actually arises is the additive identity on the right:
// `x + 0` and `x - 0` are the same value, whichever way the flip goes.
// The left operand is not symmetric — `0 + x` and `0 - x` differ — and
// the comparison and boolean operators have no such identity at all, so
// only a right-hand literal zero is rejected.
func equivalent(masked string, op operator, end int) bool {
	if op.from != "+" && op.from != "-" {
		return false
	}
	right := strings.TrimLeft(masked[end:], " \t")
	if i := strings.IndexAny(right, " \t"); i >= 0 {
		right = right[:i]
	}
	return right == "0"
}

// Mutate lists every operator flip on one line, skipping comments and
// the insides of string literals.
func Mutate(path string, line int, text string) []Mutant {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "*") || strings.HasPrefix(trimmed, "/*") {
		return nil
	}
	masked := maskStrings(text)
	var out []Mutant
	for _, op := range operators {
		for _, loc := range op.re.FindAllStringSubmatchIndex(masked, -1) {
			// Locate the operator itself inside the match (groups shift it).
			start := loc[0]
			end := loc[1]
			if i := strings.Index(masked[loc[0]:loc[1]], op.from); i >= 0 {
				start = loc[0] + i
				end = start + len(op.from)
			}
			if equivalent(masked, op, end) {
				continue
			}
			mutated := text[:start] + op.to + text[end:]
			out = append(out, Mutant{Path: path, Line: line, From: op.from, To: op.to, Before: trimmed, After: strings.TrimSpace(mutated)})
		}
	}
	return out
}

// maskStrings replaces the contents of quoted literals with spaces so
// operators inside them are not mutated. Comment tails are masked too.
func maskStrings(s string) string {
	b := []byte(s)
	var quote byte
	for i := 0; i < len(b); i++ {
		c := b[i]
		switch {
		case quote != 0:
			if c == '\\' && i+1 < len(b) {
				b[i], b[i+1] = ' ', ' '
				i++
				continue
			}
			if c == quote {
				quote = 0
				continue
			}
			b[i] = ' '
		case c == '"' || c == '\'' || c == '`':
			quote = c
		case c == '/' && i+1 < len(b) && b[i+1] == '/', c == '#':
			for j := i; j < len(b); j++ {
				b[j] = ' '
			}
			return string(b)
		}
	}
	return string(b)
}

// Changed lists the added or modified lines of tracked source files
// relative to HEAD, plus every line of untracked source files. Test files
// are excluded. The map is path → sorted 1-based lines, each path relative
// to root — which need not be the repository's top: git diff names files
// from the top unless told --relative, while ls-files already answers
// relative to the directory it runs in, and the two have to agree.
func Changed(ctx context.Context, root string) (map[string][]int, error) {
	out := map[string][]int{}
	// A repository with no commit yet has no HEAD to diff against: every
	// file is untracked, and ls-files below finds them. rev-parse -q
	// exits 1 for a missing revision and 128 for anything worse.
	_, err := gitx.Run(ctx, root, nil, "rev-parse", "--verify", "-q", "HEAD^{commit}")
	switch {
	case err == nil:
		diff, err := gitx.Run(ctx, root, nil, "diff", "-U0", "--no-color", "--relative", "HEAD", "--", ".")
		if err != nil {
			return nil, err
		}
		parseDiff(diff, out)
	case gitx.ExitCode(err) != 1:
		return nil, err
	}
	untracked, err := gitx.Run(ctx, root, nil, "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	for _, p := range strings.Split(strings.TrimSpace(untracked), "\n") {
		if p == "" || !sourceExt[filepath.Ext(p)] || project.IsTestFile(p) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, p))
		if err != nil {
			continue
		}
		n := strings.Count(string(data), "\n")
		if len(data) > 0 && data[len(data)-1] != '\n' {
			n++
		}
		for i := 1; i <= n; i++ {
			out[p] = append(out[p], i)
		}
	}
	for p, lines := range out {
		sort.Ints(lines)
		out[p] = lines
	}
	return out, nil
}

var reHunk = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@`)

func parseDiff(diff string, out map[string][]int) {
	var path string
	sc := bufio.NewScanner(strings.NewReader(diff))
	sc.Buffer(make([]byte, 1<<16), 16<<20)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "+++ "):
			path = strings.TrimPrefix(strings.TrimPrefix(line, "+++ "), "b/")
			if path == "/dev/null" || !sourceExt[filepath.Ext(path)] || project.IsTestFile(path) {
				path = ""
			}
		case strings.HasPrefix(line, "@@"):
			if path == "" {
				continue
			}
			m := reHunk.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			start, _ := strconv.Atoi(m[1])
			count := 1
			if m[2] != "" {
				count, _ = strconv.Atoi(m[2])
			}
			for i := 0; i < count; i++ {
				out[path] = append(out[path], start+i)
			}
		}
	}
}

// Generate builds mutants for the changed lines, at most max, spread
// across files and lines rather than clustered at the top of one file.
func Generate(root string, changed map[string][]int, max int) ([]Mutant, error) {
	var all []Mutant
	paths := make([]string, 0, len(changed))
	for p := range changed {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		data, err := os.ReadFile(filepath.Join(root, p))
		if err != nil {
			return nil, err
		}
		lines := strings.Split(string(data), "\n")
		for _, n := range changed[p] {
			if n < 1 || n > len(lines) {
				continue
			}
			all = append(all, Mutate(p, n, lines[n-1])...)
		}
	}
	if max > 0 && len(all) > max {
		picked := make([]Mutant, 0, max)
		for i := 0; i < max; i++ {
			picked = append(picked, all[i*len(all)/max])
		}
		all = picked
	}
	return all, nil
}

// Outcome is what happened to one mutant.
type Outcome struct {
	Mutant
	Killed   bool
	TimedOut bool
	Output   string // tail of the test output when killed
}

// Report is the result of a run.
type Report struct {
	Command  string
	Total    int
	Killed   int
	Survived []Outcome
	TimedOut int
	Duration time.Duration
	// Skipped says why nothing ran (no changed lines, no mutants).
	Skipped string
}

// OK reports whether every mutant was caught (or nothing needed running).
func (r Report) OK() bool { return len(r.Survived) == 0 }

// Summary is one line for notices.
func (r Report) Summary() string {
	if r.Skipped != "" {
		return "mutation testing skipped: " + r.Skipped
	}
	s := fmt.Sprintf("mutation testing: %d/%d mutants killed", r.Killed, r.Total)
	if r.TimedOut > 0 {
		s += fmt.Sprintf(" (%d by timeout)", r.TimedOut)
	}
	if len(r.Survived) > 0 {
		s += fmt.Sprintf(", %d survived", len(r.Survived))
	}
	return s + " (" + r.Duration.Round(100*time.Millisecond).String() + ")"
}

// mutationExtra is the JSON shape of the Extra field on a
// session.TypeMutation event (see Event): the numbers a proof record
// needs, without the per-mutant detail Feedback prints.
type mutationExtra struct {
	Killed   int    `json:"killed"`
	Survived int    `json:"survived"`
	Total    int    `json:"total"`
	Command  string `json:"command"`
}

// Event turns a completed run into the session event that records it, so
// a session's log — and from it a proof record (see internal/proof and
// internal/agent's buildProof) — knows the mutation score for the turn
// that produced it. Call it only for a run that actually mutated
// something; a skipped run (r.Skipped != "") has no score to log.
func (r Report) Event() session.Event {
	extra, _ := json.Marshal(mutationExtra{Killed: r.Killed, Survived: len(r.Survived), Total: r.Total, Command: r.Command})
	return session.Event{Type: session.TypeMutation, Extra: extra}
}

// Scored reports whether mutants ran, so Event has a score to record.
func (r Report) Scored() bool { return r.Skipped == "" }

// Feedback tells the model which changed lines the tests do not pin down.
func (r Report) Feedback() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "The tests do not catch %d of %d small mutations of the lines you changed (`%s` still passes with each of these in place):\n\n", len(r.Survived), r.Total, r.Command)
	for _, o := range r.Survived {
		fmt.Fprintf(&sb, "- %s\n    was: %s\n    now: %s\n", o.Mutant, o.Before, o.After)
	}
	sb.WriteString("\nFor each one, either add or strengthen a test that fails with the mutation, or, if the mutation is genuinely equivalent to the original, say so explicitly. Do not disable the check.")
	return sb.String()
}

// Runner runs the mutants against a command in a throwaway worktree of
// the repository's current state (including uncommitted changes).
type Runner struct {
	Root    string
	Command string
	// Timeout per mutant; zero means 5 minutes. A mutant that times out
	// counts as killed: an infinite loop is a detected change.
	Timeout time.Duration
	// Max mutants; zero means DefaultMax.
	Max int
	// Log receives progress lines when set.
	Log func(string)
}

// DefaultMax bounds the mutants per run: each one is a full test run.
const DefaultMax = 12

// Run generates mutants for the changed lines and tests each one.
func (r *Runner) Run(ctx context.Context) (Report, error) {
	rep := Report{Command: r.Command}
	changed, err := Changed(ctx, r.Root)
	if err != nil {
		return rep, err
	}
	if len(changed) == 0 {
		rep.Skipped = "no changed source lines"
		return rep, nil
	}
	max := r.Max
	if max <= 0 {
		max = DefaultMax
	}
	mutants, err := Generate(r.Root, changed, max)
	if err != nil {
		return rep, err
	}
	if len(mutants) == 0 {
		rep.Skipped = "no operators to flip on the changed lines"
		return rep, nil
	}
	return r.runMutants(ctx, mutants)
}

// runMutants checks out the tree as it stands into a throwaway worktree
// and tests each mutant in it, one at a time. It is Run's second half:
// Run decides *what* to mutate (which needs the repository's diff), this
// decides what each mutation proves (which needs only the mutants and a
// place to apply them), and splitting them lets the execution half be
// tested without a diff to mine.
func (r *Runner) runMutants(ctx context.Context, mutants []Mutant) (Report, error) {
	start := time.Now()
	rep := Report{Command: r.Command, Total: len(mutants)}
	repo, err := snapshot.Open(ctx, r.Root)
	if err != nil {
		return rep, err
	}
	snap, err := repo.Take(ctx, "mutation base")
	if err != nil {
		return rep, err
	}
	// Dropping the snapshot has to work after ctx is cancelled — that is
	// when it matters most — but it must not be able to hang a cancelled
	// run forever either, which a bare context.Background() can.
	defer func() {
		dctx, dcancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer dcancel()
		repo.Drop(dctx, snap)
	}()
	// The mutants' paths are relative to r.Root, the worktree is a
	// checkout of the repository's top, and the project may be a
	// subdirectory of it: the files and the command's directory are both
	// found under that same prefix in the checkout.
	prefix, err := gitx.Run(ctx, r.Root, nil, "rev-parse", "--show-prefix")
	if err != nil {
		return rep, err
	}
	top, cleanup, err := worktree.Detached(ctx, "mut", repo.Root, snap.Commit, nil)
	if err != nil {
		return rep, err
	}
	defer cleanup()
	dir := filepath.Join(top, filepath.FromSlash(prefix))
	// Untracked files are not in the snapshot's tree only if ignored;
	// snapshot.Take adds everything else. Copy nothing more.
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	for i, m := range mutants {
		if ctx.Err() != nil {
			return rep, ctx.Err()
		}
		rel, err := filepath.Rel(r.Root, filepath.Join(r.Root, m.Path))
		if err != nil {
			return rep, err
		}
		target := filepath.Join(dir, rel)
		original, err := os.ReadFile(target)
		if err != nil {
			return rep, fmt.Errorf("mutant %s: %w", m, err)
		}
		lines := strings.Split(string(original), "\n")
		// The worktree is a checkout of a snapshot taken after Generate
		// read the working tree, so the line should be the one the mutant
		// was built from. When it is not — a concurrent edit, a snapshot
		// that did not capture something — mutating by line number would
		// silently corrupt an unrelated line and score the result as if
		// it meant something. Skip it instead.
		// A file that has since grown shorter no longer holds the line at all;
		// that is the same case, and is not scored either.
		if m.Line < 1 || m.Line > len(lines) || strings.TrimSpace(lines[m.Line-1]) != m.Before {
			rep.Total--
			if r.Log != nil {
				r.Log(fmt.Sprintf("mutant %d/%d skipped: %s no longer holds the recorded line", i+1, len(mutants), m))
			}
			continue
		}
		indent := lines[m.Line-1][:len(lines[m.Line-1])-len(strings.TrimLeft(lines[m.Line-1], " \t"))]
		lines[m.Line-1] = indent + m.After
		if err := os.WriteFile(target, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
			return rep, err
		}
		o := Outcome{Mutant: m}
		cctx, cancel := context.WithTimeout(ctx, timeout)
		cmd := shell.Command(cctx, r.Command)
		// Supervise is what makes the timeout real: without a process
		// group the context kills only the shell, and Wait then blocks on
		// the output pipes for as long as a leaked grandchild holds them
		// (a test binary that starts a server and forgets it) — the run
		// hung forever, past its own timeout and past ctrl+c.
		shell.Supervise(cmd)
		cmd.Dir = dir
		// Scrubbed of the agent's credentials, as verify's commands are:
		// this runs the tests the model just wrote.
		cmd.Env = append(shell.ScrubEnv(os.Environ()), "JANETCODE_VERIFY=1", "JANETCODE_MUTANT="+m.String())
		out, runErr := cmd.CombinedOutput()
		// A test command that exits 0 but leaks a grandchild holding its
		// pipes (a server a package test starts and forgets) surfaces as
		// ErrWaitDelay once the bounded pipe wait cuts the read — the
		// command itself passed, which must not count as killing the
		// mutant.
		if errors.Is(runErr, exec.ErrWaitDelay) {
			runErr = nil
		}
		o.TimedOut = errors.Is(cctx.Err(), context.DeadlineExceeded)
		cancel()
		// Every later mutant is applied to this same file. If the
		// original cannot be put back, they would all run against a tree
		// that still carries this mutation, and every one of their
		// verdicts would be about the wrong code — so stop rather than
		// report a score built on it.
		if werr := os.WriteFile(target, original, 0o644); werr != nil {
			return rep, fmt.Errorf("restore %s after mutant %s: %w", m.Path, m, werr)
		}
		if runErr != nil || o.TimedOut {
			o.Killed = true
			rep.Killed++
			if o.TimedOut {
				rep.TimedOut++
			}
			o.Output = textfmt.TailBytes(strings.TrimSpace(string(out)), 400)
		} else {
			rep.Survived = append(rep.Survived, o)
		}
		if r.Log != nil {
			state := "killed"
			if !o.Killed {
				state = "SURVIVED"
			} else if o.TimedOut {
				state = "killed (timeout)"
			}
			r.Log(fmt.Sprintf("mutant %d/%d %s: %s", i+1, len(mutants), state, m))
		}
	}
	rep.Duration = time.Since(start)
	return rep, nil
}
