// Package whybroke bisects over a session's turns, not its commits: given
// a test that fails now, it finds the first point in the session after
// which it fails, and shows that point's intent — the turn's prompt and
// the reason the model gave for the change.
//
// A session can land as one squashed commit, so git bisect stops at the
// commit; this goes inside it. The states it searches are rebuilt from
// what the session recorded, with no model call and no tokens:
//
//   - change by change (the default): the session's start state
//     (session.StartState's snapshot) with the provenance record's file
//     changes applied up to the k-th, each file at the content the k-th
//     change left it (internal/agent's Change: Before and After per call),
//     so the culprit is one tool call, inside a turn;
//   - turn by turn (the fallback): when the start plus every recorded
//     change still passes, the break came from something the provenance
//     record does not hold — a shell command, a hand edit — and the
//     search moves to the snapshots taken before each turn's first change
//     (the /undo checkpoints), with the work tree as it is now at the end.
//
// Each state is exported into a throwaway directory and the test is run
// there; the user's work tree, HEAD and index are never touched. When the
// culprit change landed in a commit that carries a proof note (internal/
// proof), the report names that commit and how many turns it squashes.
//
// cmd/janetcode's --why-broke flag only parses arguments and prints
// Report.Text() or Report.JSON().
package whybroke

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/change"
	"github.com/xmonader/janetcode/internal/gitx"
	"github.com/xmonader/janetcode/internal/proof"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/shell"
	"github.com/xmonader/janetcode/internal/snapshot"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// DefaultTimeout bounds one run of the test.
const DefaultTimeout = 5 * time.Minute

// Options configures one bisection.
type Options struct {
	// Test is a test name (TestParse, tests/test_a.py::test_b) or, when it
	// has a space in it, the shell command to run as the test.
	Test string
	// SessionPath is the session's .jsonl file (see whatif.ResolveSession).
	SessionPath string
	// Timeout bounds one run of the test; 0 is DefaultTimeout.
	Timeout time.Duration
	// Log, when set, receives one line per probe.
	Log func(format string, args ...any)
}

// ErrBadArgument marks an error that is the caller's input rather than a
// failure along the way, for the usage exit status.
var ErrBadArgument = errors.New("bad why-broke argument")

func badArg(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrBadArgument, fmt.Sprintf(format, args...))
}

// Probe is one run of the test against one rebuilt state.
type Probe struct {
	State  int    `json:"state"`
	Label  string `json:"label"`
	Failed bool   `json:"failed"`
}

// Culprit is the step after which the test fails.
type Culprit struct {
	// Seq, Path, Tool and Intent name the change (change mode; Seq is 0
	// in turn mode, where the turn is the finest grain there is).
	Seq    int    `json:"seq,omitempty"`
	Path   string `json:"path,omitempty"`
	Tool   string `json:"tool,omitempty"`
	Intent string `json:"intent,omitempty"`
	Diff   string `json:"diff,omitempty"`
	// Turn is the turn it belongs to and Prompt that turn's request.
	Turn   int    `json:"turn"`
	Prompt string `json:"prompt,omitempty"`
	// TurnChanges are the turn's other changes, for context.
	TurnChanges []string `json:"turn_changes,omitempty"`
	// Commit is the commit the change landed in, when a proof note says
	// so, and Squashed how many of the session's turns that commit holds.
	Commit   string `json:"commit,omitempty"`
	Squashed int    `json:"squashed,omitempty"`
	// Output is the tail of the test's output at the first failing state.
	Output string `json:"output,omitempty"`
}

// Report is a bisection's result.
type Report struct {
	SessionID string `json:"session_id"`
	Command   string `json:"command"`
	// Mode is "changes" or "turns" (see the package doc).
	Mode    string   `json:"mode"`
	States  int      `json:"states"`
	Probes  []Probe  `json:"probes"`
	Culprit *Culprit `json:"culprit,omitempty"`
	// Verdict says what was found when there is no culprit: the test
	// passes now, or already failed when the session started.
	Verdict string   `json:"verdict"`
	Notes   []string `json:"notes,omitempty"`
}

// state is one point the bisection can rebuild and test.
type state struct {
	label string
	// build writes the state into dir, an empty directory.
	build func(ctx context.Context, dir string) error
}

// Run bisects. See the package doc.
func Run(ctx context.Context, opts Options) (Report, error) {
	logf := opts.Log
	if logf == nil {
		logf = func(string, ...any) {}
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	sess, err := openSession(opts.SessionPath)
	if err != nil {
		return Report{}, err
	}
	if sess.meta.Start == nil || sess.meta.Start.Snap == "" {
		return Report{}, badArg("session %s has no recorded start state (it began outside a git repository, before any commit, or before start states were recorded)", sess.meta.ID)
	}
	repo, err := snapshot.Open(ctx, sess.meta.Cwd)
	if err != nil {
		return Report{}, fmt.Errorf("the session's directory %s: %w", sess.meta.Cwd, err)
	}
	cmd, err := Command(repo.Root, opts.Test)
	if err != nil {
		return Report{}, err
	}
	r := Report{SessionID: sess.meta.ID, Command: cmd}
	start := snapshot.Snap{Commit: sess.meta.Start.Snap, Head: sess.meta.Start.Head}

	runAt := func(st state) (bool, string, error) {
		dir, derr := os.MkdirTemp("", "janetcode-whybroke-")
		if derr != nil {
			return false, "", derr
		}
		defer os.RemoveAll(dir)
		if berr := st.build(ctx, dir); berr != nil {
			return false, "", fmt.Errorf("rebuild %s: %w", st.label, berr)
		}
		sub := dir
		if rel, rerr := filepath.Rel(repo.Root, sess.meta.Cwd); rerr == nil && !strings.HasPrefix(rel, "..") {
			sub = filepath.Join(dir, rel)
		}
		failed, out := runTest(ctx, sub, cmd, timeout)
		return failed, out, ctx.Err()
	}

	// Change by change first.
	changes := liveChanges(sess.changes)
	r.Mode = "changes"
	states := []state{{label: "session start", build: func(ctx context.Context, dir string) error { return repo.Export(ctx, start, dir) }}}
	for i, c := range changes {
		k := i + 1
		states = append(states, state{
			label: fmt.Sprintf("after change #%d (turn %d, %s %s)", c.Seq, c.Turn, c.Tool, relTo(repo.Root, c.Path)),
			build: func(ctx context.Context, dir string) error {
				if err := repo.Export(ctx, start, dir); err != nil {
					return err
				}
				return materialize(repo.Root, dir, changes, k)
			},
		})
	}
	culprit, out, err := bisect(states, runAt, &r, logf)
	if err != nil {
		return r, err
	}
	if culprit == 0 && r.Verdict == "passes" {
		// Start plus every recorded change passes: try the snapshots,
		// which hold what the shell did too, ending at the tree now.
		turnStates, cps, notes, drop, terr := turnStates(ctx, repo, start, sess)
		defer drop()
		r.Notes = append(r.Notes, notes...)
		if terr != nil {
			return r, terr
		}
		r.Notes = append(r.Notes, "the session's start plus every change its file tools recorded passes, so the search moved to the snapshots taken before each turn, which also hold what shell commands changed")
		r.Mode, r.Probes, r.Verdict = "turns", nil, ""
		culprit, out, err = bisect(turnStates, runAt, &r, logf)
		if err != nil {
			return r, err
		}
		if culprit > 0 {
			r.Culprit = turnCulprit(sess, cps, culprit, repo.Root)
		}
	} else if culprit > 0 {
		r.Culprit = changeCulprit(sess, changes[culprit-1], changes, repo.Root)
		r.Culprit.Commit, r.Culprit.Squashed = findCommit(ctx, repo.Root, sess.meta.ID, changes[culprit-1].Seq)
	}
	if r.Culprit != nil {
		r.Culprit.Output = textfmt.TailBytes(strings.TrimSpace(out), 3<<10)
		r.Verdict = "found"
	}
	return r, nil
}

// bisect finds the first failing state, assuming the first passes and
// the last fails, and checks both ends first. It returns the index of the
// first failing state (0 when there is none, with r.Verdict saying why)
// and the test's output there.
func bisect(states []state, runAt func(state) (bool, string, error), r *Report, logf func(string, ...any)) (int, string, error) {
	r.States = len(states)
	outs := map[int]string{}
	probe := func(i int) (bool, error) {
		failed, out, err := runAt(states[i])
		if err != nil {
			return false, err
		}
		outs[i] = out
		r.Probes = append(r.Probes, Probe{State: i, Label: states[i].label, Failed: failed})
		logf("%s: %s", states[i].label, map[bool]string{true: "fails", false: "passes"}[failed])
		return failed, nil
	}
	last := len(states) - 1
	failed, err := probe(last)
	if err != nil {
		return 0, "", err
	}
	if !failed {
		r.Verdict = "passes"
		return 0, "", nil
	}
	if last == 0 {
		r.Verdict = "failed at start"
		return 0, "", nil
	}
	failed, err = probe(0)
	if err != nil {
		return 0, "", err
	}
	if failed {
		r.Verdict = "failed at start"
		return 0, "", nil
	}
	lo, hi := 0, last // lo passes, hi fails
	for hi-lo > 1 {
		mid := (lo + hi) / 2
		f, err := probe(mid)
		if err != nil {
			return 0, "", err
		}
		if f {
			hi = mid
		} else {
			lo = mid
		}
	}
	return hi, outs[hi], nil
}

// materialize writes, into dir (the start state), every path the first k
// changes touched at the content the latest of them left it — and a path
// they touched only later back at what the first change to it found.
func materialize(root, dir string, changes []change.Change, k int) error {
	type at struct {
		data   []byte
		exists bool
	}
	want := map[string]at{}
	for i, c := range changes {
		p := relTo(root, c.Path)
		if strings.HasPrefix(p, "..") || filepath.IsAbs(p) {
			continue // outside the repository: not in the export either
		}
		if i < k {
			want[p] = at{c.After, !c.Gone}
		} else if _, seen := want[p]; !seen {
			want[p] = at{c.Before, c.Existed}
		}
	}
	paths := make([]string, 0, len(want))
	for p := range want {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		w := want[p]
		full := filepath.Join(dir, filepath.FromSlash(p))
		if !w.exists {
			if err := os.Remove(full); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, w.data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// liveChanges is the provenance record in order, without changes since
// undone: an undone change is not in the tree the session ended with.
func liveChanges(cs []change.Change) []change.Change {
	var out []change.Change
	for _, c := range cs {
		if !c.Undone {
			out = append(out, c)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out
}

// checkpoint is the part of an /undo checkpoint the turn mode reads.
type checkpoint struct {
	Turn int            `json:"turn"`
	Snap *snapshot.Snap `json:"snap"`
}

// turnStates are the turn mode's states: the session's start, the
// snapshot before each later turn's first change, and the work tree now.
// drop releases the snapshot taken of the work tree.
func turnStates(ctx context.Context, repo *snapshot.Repo, start snapshot.Snap, sess *recorded) (states []state, kept []checkpoint, notes []string, drop func(), err error) {
	drop = func() {}
	var cps []checkpoint
	for _, c := range sess.checkpoints {
		if c.Snap != nil && c.Snap.Commit != "" && c.Turn > 0 {
			cps = append(cps, c)
		}
	}
	sort.SliceStable(cps, func(i, j int) bool { return cps[i].Turn < cps[j].Turn })
	export := func(s snapshot.Snap) func(context.Context, string) error {
		return func(ctx context.Context, dir string) error { return repo.Export(ctx, s, dir) }
	}
	states = []state{{label: "session start", build: export(start)}}
	for i, c := range cps {
		if i == 0 && c.Snap.Commit == start.Commit {
			continue
		}
		states = append(states, state{label: fmt.Sprintf("before turn %d", c.Turn), build: export(*c.Snap)})
		kept = append(kept, c)
	}
	now, err := repo.Take(ctx, "why-broke")
	if err != nil {
		return nil, nil, notes, drop, fmt.Errorf("snapshot the work tree: %w", err)
	}
	drop = func() { repo.Drop(context.Background(), now) }
	states = append(states, state{label: "the work tree now", build: export(now)})
	if len(cps) == 0 {
		notes = append(notes, "the session recorded no per-turn snapshots; only its start and the tree now could be compared")
	}
	return states, kept, notes, drop, nil
}

// changeCulprit describes the change after which the test fails.
func changeCulprit(sess *recorded, c change.Change, all []change.Change, root string) *Culprit {
	k := &Culprit{Seq: c.Seq, Path: relTo(root, c.Path), Tool: c.Tool, Intent: c.Intent, Turn: c.Turn, Prompt: sess.prompt(c.Turn)}
	k.Diff = textfmt.ClipBytes(unified(c), 4<<10)
	for _, o := range all {
		if o.Turn == c.Turn && o.Seq != c.Seq {
			k.TurnChanges = append(k.TurnChanges, fmt.Sprintf("#%d %s%s", o.Seq, relTo(root, o.Path), intentOf(o.Intent)))
		}
	}
	return k
}

// turnCulprit describes the turn between state i-1 and state i (see
// turnStates): the turn state i-1 was taken before, or the session's
// first turn when state i-1 is its start.
func turnCulprit(sess *recorded, kept []checkpoint, i int, root string) *Culprit {
	turn := firstTurn(sess)
	if i >= 2 && i-2 < len(kept) {
		turn = kept[i-2].Turn
	}
	k := &Culprit{Turn: turn, Prompt: sess.prompt(turn)}
	for _, o := range liveChanges(sess.changes) {
		if o.Turn == turn {
			k.TurnChanges = append(k.TurnChanges, fmt.Sprintf("#%d %s%s", o.Seq, relTo(root, o.Path), intentOf(o.Intent)))
		}
	}
	return k
}

func firstTurn(sess *recorded) int {
	for _, t := range sess.turns {
		return t.Turn
	}
	return 1
}

// findCommit is the commit, among the last 200 on HEAD, whose proof note
// records change seq of session id, and how many of the session's turns
// that note holds.
func findCommit(ctx context.Context, root, id string, seq int) (string, int) {
	out, err := gitx.Run(ctx, root, nil, "log", "--format=%H", "-n", "200", "HEAD")
	if err != nil {
		return "", 0
	}
	for _, sha := range strings.Fields(out) {
		rec, ok, err := proof.Read(ctx, root, sha)
		if err != nil || !ok {
			continue
		}
		for _, f := range rec.Files {
			for _, c := range f.Changes {
				sid := c.Session
				if sid == "" {
					sid = rec.Session
				}
				if sid == id && c.Seq == seq {
					turns := 0
					for _, t := range rec.Turns {
						if t.Session == "" && rec.Session == id || t.Session == id {
							turns++
						}
					}
					return sha, turns
				}
			}
		}
	}
	return "", 0
}

// Command is the shell command that runs test in root: test itself when
// it has a space in it, else the one-test form of the runner root uses —
// go test -run for a Go module, pytest for Python.
func Command(root, test string) (string, error) {
	test = strings.TrimSpace(test)
	if test == "" {
		return "", badArg("name the failing test, or give the command that runs it")
	}
	if strings.ContainsAny(test, " \t") {
		return test, nil
	}
	if strings.ContainsAny(test, `'"$;&|<>`+"`") {
		return "", badArg("%q is not a test name; give the command that runs it instead", test)
	}
	switch {
	case exists(filepath.Join(root, "go.mod")) && goTestName.MatchString(test):
		parts := strings.Split(test, "/")
		for i, p := range parts {
			parts[i] = "^" + regexp.QuoteMeta(p) + "$"
		}
		return "go test -count=1 -run '" + strings.Join(parts, "/") + "' ./...", nil
	case pythonProject(root):
		if strings.Contains(test, "::") || strings.HasSuffix(test, ".py") {
			return "python3 -m pytest -q '" + test + "'", nil
		}
		return "python3 -m pytest -q -k '" + test + "'", nil
	}
	return "", badArg("cannot tell how to run %q here: give the command, e.g. --why-broke 'npm test -- -t %s'", test, test)
}

var goTestName = regexp.MustCompile(`^(Test|Example|Fuzz|Benchmark)\w*(/\S+)?$`)

func pythonProject(root string) bool {
	for _, f := range []string{"pyproject.toml", "setup.py", "setup.cfg", "pytest.ini", "tox.ini", "conftest.py"} {
		if exists(filepath.Join(root, f)) {
			return true
		}
	}
	m, _ := filepath.Glob(filepath.Join(root, "test_*.py"))
	n, _ := filepath.Glob(filepath.Join(root, "tests", "test_*.py"))
	return len(m)+len(n) > 0
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// runTest runs cmd in dir; failed is a non-zero exit or a timeout.
func runTest(ctx context.Context, dir, cmd string, timeout time.Duration) (failed bool, out string) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	c := exec.CommandContext(ctx, "sh", "-c", cmd)
	// Without a process group the timeout kills the shell alone, and the
	// wait then blocks on the output pipe for as long as anything the
	// test started still holds it: one probe of a test that leaks a
	// server hung the whole bisection. Supervise kills the group and
	// bounds that wait. The environment is scrubbed of the agent's
	// credentials, as verify's commands are: this runs the project's
	// tests at states the model wrote.
	shell.Supervise(c)
	c.Env = shell.ScrubEnv(os.Environ())
	c.Dir = dir
	b, err := c.CombinedOutput()
	out = string(b)
	if ctx.Err() == context.DeadlineExceeded {
		out += fmt.Sprintf("\n[timed out after %s]", timeout)
	}
	return err != nil, out
}

func relTo(root, p string) string {
	if !filepath.IsAbs(p) {
		return filepath.ToSlash(p)
	}
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return p
	}
	return filepath.ToSlash(rel)
}

func intentOf(s string) string {
	if s = strings.TrimSpace(s); s == "" {
		return ""
	}
	return " — " + textfmt.Clip(s, 100)
}

// unified is the change as a unified diff, the hunks /changes shows.
func unified(c change.Change) string {
	var b strings.Builder
	for _, h := range c.Hunks() {
		b.WriteString(h.String())
	}
	return b.String()
}

// Text renders the report for a terminal.
func (r Report) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "why-broke: %s, session %s, %s over %d states\n\n", r.Command, r.SessionID, r.Mode, r.States)
	for _, p := range r.Probes {
		fmt.Fprintf(&b, "  %-6s %s\n", map[bool]string{true: "fails", false: "passes"}[p.Failed], p.Label)
	}
	b.WriteString("\n")
	switch r.Verdict {
	case "passes":
		b.WriteString("the test passes in the tree the session left: nothing to bisect\n")
	case "failed at start":
		b.WriteString("the test already failed when the session started: the session did not break it\n")
	}
	if k := r.Culprit; k != nil {
		if k.Seq > 0 {
			fmt.Fprintf(&b, "first failing after change #%d, turn %d: %s %s\n", k.Seq, k.Turn, k.Tool, k.Path)
			if k.Intent != "" {
				fmt.Fprintf(&b, "intent: %s\n", k.Intent)
			}
		} else {
			fmt.Fprintf(&b, "first failing after turn %d\n", k.Turn)
		}
		if k.Prompt != "" {
			fmt.Fprintf(&b, "turn %d asked: %s\n", k.Turn, textfmt.Clip(k.Prompt, 300))
		}
		if k.Commit != "" {
			fmt.Fprintf(&b, "landed in commit %s", proof.Short(k.Commit))
			if k.Squashed > 1 {
				fmt.Fprintf(&b, ", which squashes %d turns of this session", k.Squashed)
			}
			b.WriteString("\n")
		}
		if len(k.TurnChanges) > 0 {
			b.WriteString("the turn's other changes:\n")
			for _, c := range k.TurnChanges {
				fmt.Fprintf(&b, "  %s\n", c)
			}
		}
		if k.Diff != "" {
			fmt.Fprintf(&b, "\n%s\n", strings.TrimRight(k.Diff, "\n"))
		}
		if k.Output != "" {
			fmt.Fprintf(&b, "\ntest output there:\n%s\n", k.Output)
		}
	}
	for _, n := range r.Notes {
		fmt.Fprintf(&b, "\nnote: %s\n", n)
	}
	return b.String()
}

// JSON renders the report as indented JSON.
func (r Report) JSON() ([]byte, error) { return json.MarshalIndent(r, "", "  ") }

// recorded is what Run reads of a session: its meta, events, provenance
// record and checkpoints.
type recorded struct {
	meta        session.Meta
	turns       []session.TurnPrompt
	changes     []change.Change
	checkpoints []checkpoint
}

func (s *recorded) prompt(turn int) string {
	for _, t := range s.turns {
		if t.Turn == turn {
			return strings.TrimSpace(t.Text)
		}
	}
	return ""
}

// openSession reads a session file and its undo sidecar without opening
// the session for writing: --why-broke must work while another janetcode
// holds it.
func openSession(path string) (*recorded, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	s := &recorded{}
	var events []session.Event
	for _, line := range strings.Split(string(data), "\n") {
		var head struct {
			Type string       `json:"type"`
			Meta session.Meta `json:"meta"`
		}
		if json.Unmarshal([]byte(line), &head) != nil {
			continue
		}
		switch head.Type {
		case "meta":
			s.meta = head.Meta
		case "event":
			var e session.Event
			if json.Unmarshal([]byte(line), &e) == nil {
				events = append(events, e)
			}
		}
	}
	if s.meta.ID == "" {
		return nil, badArg("%s is not a session log", path)
	}
	s.turns = session.Turns(events)
	undo := strings.TrimSuffix(path, ".jsonl") + ".undo.json"
	if b, err := os.ReadFile(undo); err == nil {
		var f struct {
			Checkpoints []checkpoint    `json:"checkpoints"`
			Changes     []change.Change `json:"changes"`
		}
		if json.Unmarshal(b, &f) == nil {
			s.changes, s.checkpoints = f.Changes, f.Checkpoints
		}
	}
	return s, nil
}
