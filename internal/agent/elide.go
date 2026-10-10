package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/shell"
	"github.com/xmonader/janetcode/internal/textfmt"
	"github.com/xmonader/janetcode/internal/tool"
	"github.com/xmonader/janetcode/internal/verify"
)

// Round-trip elision (ElideChecks). The call a coding model makes after a
// reply that only edited is, most of the time, the test run: it cannot
// know whether the edit worked until it asks, and asking costs a whole
// model call, which is where nearly all of a turn's wall time goes. So
// after a step whose last change was a file edit, with no check after it,
// the harness runs the project's check itself and puts a compact result
// beside the step's own tool results: a pass is one line, a failure is
// shown in full up to elideBudget. The model's next reply starts with the
// evidence instead of spending a round trip to fetch it.
//
// The check is the project's verify commands when there are any (never a
// held-out acceptance command: a Display label marks one, and running it
// after every edit would hand the model the hidden tests). Without them it
// is the last project check the model ran itself this session, run as an
// ordinary bash call and only when calls are auto-approved, so the harness
// never puts a prompt in front of the user that nobody asked for. A
// `go test ./...` is narrowed to the packages the edited Go files reach
// when that is less than half the module (elideScope).
//
// The model may still run the same command; a bash call identical to one
// the harness just ran, with nothing changed since, is answered from the
// result instead of being run again, and says so. Any mutating call drops
// the cache, and so does a change to an edited file's size or time, which
// is how an edit from outside the turn is noticed.
//
// Opt-in (config elide_checks): unasked-for output changes what the loop
// sees, and the loop has been measured sensitive to far smaller changes.

// elideBudget bounds the failure output one elision note carries, shared
// between the commands that failed. Passes cost one line each.
const elideBudget = 4000

// elideTimeout bounds one elided command. A check that takes longer is
// one the model should decide to run, not one the harness runs unasked
// after every edit.
var elideTimeout = 2 * time.Minute

// elideState is what elision remembers between steps. It is only touched
// on the turn goroutine: the step's batch is over before it runs, and the
// cache is consulted before any of the next batch's goroutines start.
type elideState struct {
	// learned is the last project check the model ran itself, the
	// fallback when the project defines no verify commands.
	learned string
	// pending is the files edited since the last check ran, for scoping.
	pending map[string]bool
	// cache answers a repeat of a command the harness ran, keyed by
	// elideKey; it is dropped by any mutating call.
	cache map[string]string
	// stamps is the size and time of the edited files when the cache was
	// filled; a difference means the tree moved under it.
	stamps map[string]fileStamp
}

type fileStamp struct {
	size int64
	mod  time.Time
}

// SetElideChecks turns round-trip elision on or off for the steps that
// follow.
func (a *Agent) SetElideChecks(on bool) {
	a.featMu.Lock()
	a.ElideChecks = on
	a.featMu.Unlock()
}

// ElideChecksOn is ElideChecks, safe to read while a turn runs.
func (a *Agent) ElideChecksOn() bool {
	a.featMu.RLock()
	defer a.featMu.RUnlock()
	return a.ElideChecks
}

// resetElide forgets what a previous turn left: its edits answered to
// that turn's gate, and its cache to a tree the user may since have
// changed. The learned command is the session's and stays.
func (a *Agent) resetElide() {
	a.elide.pending = nil
	a.elide.cache = nil
	a.elide.stamps = nil
}

// elideInvalidate drops the cache: a mutating call is about to run.
func (a *Agent) elideInvalidate() {
	a.elide.cache = nil
	a.elide.stamps = nil
}

// elideProbeRe recognises a one-off script handed to an interpreter: the
// model's own inline asserts, which are checks but not the project's.
var elideProbeRe = regexp.MustCompile(`<<|\b(python3?|node|ruby|perl|deno|bun)\s+(-c|-e|--eval)\b`)

// elideProjectCheck reports whether a command runs the project's checks
// (a test run, a build, a linter) rather than an ad-hoc probe.
func elideProjectCheck(cmd string) bool {
	return cmd != "" && shell.IsCheck(cmd) && !elideProbeRe.MatchString(cmd)
}

// elideKey is a command in the form two calls are compared in: a leading
// cd into the project and a trailing 2>&1 do not make a different run.
func elideKey(cmd, root string) string {
	c := strings.TrimSpace(cmd)
	if root != "" {
		for _, r := range []string{root, "'" + root + "'", `"` + root + `"`, "."} {
			for _, sep := range []string{" && ", "; ", " ; "} {
				if p := "cd " + r + sep; strings.HasPrefix(c, p) {
					c = strings.TrimSpace(c[len(p):])
				}
			}
		}
	}
	return strings.TrimSpace(strings.TrimSuffix(c, " 2>&1"))
}

// elideHit answers u from the cache when it is a bash call identical to a
// command the harness ran after the last edit and nothing has moved since.
func (a *Agent) elideHit(u llm.Block) (string, bool) {
	if u.Name != "bash" || len(a.elide.cache) == 0 {
		return "", false
	}
	var in struct {
		Command    string `json:"command"`
		Background bool   `json:"background"`
		Stdin      string `json:"stdin"`
	}
	if json.Unmarshal(u.Input, &in) != nil || in.Background || in.Stdin != "" {
		return "", false
	}
	res, ok := a.elide.cache[elideKey(in.Command, a.RootDir())]
	if !ok {
		return "", false
	}
	for p, st := range a.elide.stamps {
		if stampOf(p) != st {
			a.elideInvalidate()
			return "", false
		}
	}
	return "[harness] Not run again: the harness ran this exact command after your last edit and nothing has changed since. Its result:\n" + res, true
}

func stampOf(path string) fileStamp {
	fi, err := os.Stat(path)
	if err != nil {
		return fileStamp{size: -1}
	}
	return fileStamp{size: fi.Size(), mod: fi.ModTime()}
}

// elideAfterStep is the step's elision: it notes what the step ran and
// edited, and when the step's last change was an edit nothing checked,
// runs the check and returns the note for the step's results.
func (a *Agent) elideAfterStep(ctx context.Context, uses, results []llm.Block, emit func(Event)) string {
	lastChange, checked := -1, false
	for i, u := range uses {
		if i >= len(results) {
			break
		}
		if cmd := elideCallCheck(u); cmd != "" && !strings.HasPrefix(results[i].Text, "[harness] Not run again") {
			a.elide.learned = cmd
			checked = true
		}
		t, ok := a.Tools.Get(u.Name)
		if !ok || !tool.MutatingCall(t, u.Input) {
			continue
		}
		lastChange, checked = i, elideCallCheck(u) != ""
		if editTool(u.Name) && !results[i].IsError {
			if p := editPath(u.Input); p != "" {
				if a.elide.pending == nil {
					a.elide.pending = map[string]bool{}
				}
				a.elide.pending[a.absPath(p)] = true
			}
		}
	}
	if checked {
		a.elide.pending = nil
	}
	if !a.ElideChecksOn() || lastChange < 0 || checked || ctx.Err() != nil || a.readOnly() || a.DryRunOn() {
		return ""
	}
	if u := uses[lastChange]; !editTool(u.Name) || results[lastChange].IsError || len(a.elide.pending) == 0 {
		return ""
	}
	gate, _ := a.VerifyGate()
	var note string
	switch {
	case !a.Off["verify"] && gate != nil && len(gateCommands(gate)) > 0:
		note = a.elideGate(ctx, gate)
	case a.elide.learned != "" && a.Settings().AutoApprove:
		note = a.elideLearned(ctx, uses[lastChange], emit)
	default:
		return ""
	}
	if note == "" {
		return ""
	}
	a.elide.stamps = map[string]fileStamp{}
	for p := range a.elide.pending {
		a.elide.stamps[p] = stampOf(p)
	}
	a.elide.pending = nil
	emit(Event{Kind: EvNotice, Text: "checked after the edit: " + firstLine(note)})
	a.logEvent(session.Event{Type: session.TypeNotice, Turn: a.turnN, Step: a.stepN, Name: "elide/run", Output: note})
	return note
}

// elideCallCheck is the project check a call runs, if any: a bash command
// that is one, or an edit's then_run.
func elideCallCheck(u llm.Block) string {
	var cmd string
	switch {
	case u.Name == "bash":
		cmd = shell.CommandOf(string(u.Input))
	case editTool(u.Name):
		cmd, _ = tool.TakeThenRun(u.Input)
	}
	if elideProjectCheck(cmd) {
		return cmd
	}
	return ""
}

// editPath is the path an edit tool's input names.
func editPath(input json.RawMessage) string {
	var in struct {
		Path     string `json:"path"`
		FilePath string `json:"file_path"`
	}
	_ = json.Unmarshal(input, &in)
	if in.Path != "" {
		return in.Path
	}
	return in.FilePath
}

func (a *Agent) absPath(p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(a.RootDir(), p)
}

// gateCommands are the verify commands elision may run: all but the ones
// the gate reports under another name, which are held-out checks.
func gateCommands(gate *verify.Runner) []string {
	var out []string
	for _, c := range gate.Config.Commands {
		if _, hidden := gate.Display[c]; !hidden {
			out = append(out, c)
		}
	}
	return out
}

// elideGate runs the project's verify commands, every one of them, and
// writes the note. A failure that the turn's baseline also saw is marked,
// so the model is not sent chasing what it did not break.
func (a *Agent) elideGate(ctx context.Context, gate *verify.Runner) string {
	cmds := gateCommands(gate)
	ran := make([]string, len(cmds))
	for i, c := range cmds {
		ran[i] = c
		if s := elideScope(ctx, gate.Cwd, c, a.pendingPaths()); s != "" {
			ran[i] = s
		}
	}
	timeout := elideTimeout
	if t := time.Duration(gate.Config.Timeout) * time.Second; t > 0 && t < timeout {
		timeout = t
	}
	r := &verify.Runner{Cwd: gate.Cwd, Config: verify.Config{Commands: ran, Timeout: int(timeout / time.Second)}}
	rep := r.RunEach(ctx)
	if ctx.Err() != nil || len(rep.Commands) != len(ran) {
		return ""
	}
	before := map[string]bool{}
	if b := a.usableBaseline(); b != nil {
		for _, c := range b.rep.Commands {
			if !c.OK {
				before[c.Name] = true
			}
		}
	}
	a.elide.cache = map[string]string{}
	lines := make([]string, len(ran))
	budget := elideBudget
	for i, c := range rep.Commands {
		line := "$ " + ran[i] + " — passed"
		full := "passed"
		if !c.OK {
			out := strings.TrimSpace(c.Output)
			full = fmt.Sprintf("exit %d\n%s", c.Exit, out)
			line = fmt.Sprintf("$ %s — failed (exit %d)", ran[i], c.Exit)
			if before[cmds[i]] {
				line += ", as it did before this turn's first edit"
			}
			if budget < 200 {
				line += " — output left out, the note's budget is spent"
			} else {
				shown := textfmt.TruncateTo(out, budget)
				budget -= len(shown)
				line += "\n" + shown
			}
		}
		lines[i] = line
		if ran[i] == cmds[i] {
			a.elide.cache[elideKey(cmds[i], gate.Cwd)] = full
		}
	}
	head := fmt.Sprintf("[harness] Checked after your edit (%s):", rep.Duration.Round(100*time.Millisecond))
	if len(lines) == 1 && rep.OK() {
		return head + " " + lines[0]
	}
	return head + "\n" + strings.Join(lines, "\n")
}

// elideLearned runs the model's own last check as a bash call of its own,
// under a derived id, exactly as then_run does: hooks, rules, sandbox.
func (a *Agent) elideLearned(ctx context.Context, edit llm.Block, emit func(Event)) string {
	if _, ok := a.Tools.Get("bash"); !ok {
		return ""
	}
	cmd := a.elide.learned
	ran := cmd
	if s := elideScope(ctx, a.RootDir(), cmd, a.pendingPaths()); s != "" {
		ran = s
	}
	input, _ := json.Marshal(map[string]any{"command": ran, "timeout_ms": elideTimeout.Milliseconds()})
	run := llm.Block{Type: llm.BlockToolUse, ID: edit.ID + "~elide", Name: "bash", Input: input}
	start := time.Now()
	ctx, wait := meterWaits(ctx)
	out, _, isErr, err := a.executeTool(ctx, run, emit)
	a.stats.tool(run.Name, time.Since(start)-wait.total(), isErr || err != nil)
	if err != nil || ctx.Err() != nil {
		return ""
	}
	out = strings.TrimSpace(out)
	failed := isErr || strings.Contains(lastLine(out), "[exit code")
	a.elide.cache = map[string]string{}
	if ran == cmd {
		a.elide.cache[elideKey(cmd, a.RootDir())] = out
	}
	head := fmt.Sprintf("[harness] Checked after your edit (%s): $ %s", time.Since(start).Round(100*time.Millisecond), ran)
	if !failed {
		if l := lastLine(out); l != "" {
			return head + " — passed: " + textfmt.TruncateTo(l, 200)
		}
		return head + " — passed"
	}
	return head + " — failed\n" + textfmt.TruncateTo(out, elideBudget)
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[i+1:])
	}
	return s
}

func (a *Agent) pendingPaths() []string {
	out := make([]string, 0, len(a.elide.pending))
	for p := range a.elide.pending {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// goTestAllRe finds the ./... of a `go test` run, in the segment that runs
// it, so it can be replaced by a package list.
var goTestAllRe = regexp.MustCompile(`(^|[;&|]\s*|\s)(go\s+test\b[^;&|]*?\s)\./\.\.\.(\s|$)`)

// elideScope narrows a `go test ./...` to the packages the edited files
// can reach: those that hold them and those whose code or tests import
// them, from `go list` run in dir. It returns "" to run the command as it
// is: not a Go test run, an edit that is not Go, a module go list cannot
// read, or a narrowing that would still cover half of the module — where
// the build cache already makes the full run cheap and the full run is
// the one a repeat can be answered from.
func elideScope(ctx context.Context, dir, cmd string, edited []string) string {
	if !goTestAllRe.MatchString(cmd) || len(edited) == 0 {
		return ""
	}
	for _, p := range edited {
		if filepath.Ext(p) != ".go" {
			return ""
		}
	}
	lctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	c := exec.CommandContext(lctx, "go", "list", "-e", "-f",
		`{{.ImportPath}}{{"\t"}}{{.Dir}}{{"\t"}}{{join .Deps " "}}{{"\t"}}{{join .TestImports " "}} {{join .XTestImports " "}}`, "./...")
	c.Dir = dir
	c.Env = shell.ScrubEnv(os.Environ())
	out, err := c.Output()
	if err != nil {
		return ""
	}
	pkgs := parseGoList(out)
	affected := affectedPackages(pkgs, edited)
	if len(affected) == 0 || len(affected)*2 > len(pkgs) {
		return ""
	}
	loc := goTestAllRe.FindStringSubmatchIndex(cmd)
	// Group 2 is "go test <flags> "; the ./... follows it.
	at := loc[5]
	return cmd[:at] + strings.Join(affected, " ") + cmd[at+len("./..."):]
}

// goPkg is one line of the go list output elideScope asks for.
type goPkg struct {
	path, dir   string
	deps, tests []string
}

func parseGoList(out []byte) []goPkg {
	var pkgs []goPkg
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		f := strings.Split(sc.Text(), "\t")
		if len(f) != 4 || f[0] == "" {
			continue
		}
		pkgs = append(pkgs, goPkg{path: f[0], dir: f[1], deps: strings.Fields(f[2]), tests: strings.Fields(f[3])})
	}
	return pkgs
}

// affectedPackages are the packages whose tests an edit to the files can
// change: the packages holding them, and every package whose
// dependencies, test imports or test imports' dependencies include one.
func affectedPackages(pkgs []goPkg, edited []string) []string {
	byPath := map[string]goPkg{}
	touched := map[string]bool{}
	for _, p := range pkgs {
		byPath[p.path] = p
		for _, f := range edited {
			if filepath.Dir(f) == filepath.Clean(p.dir) {
				touched[p.path] = true
			}
		}
	}
	var out []string
	for _, p := range pkgs {
		reach := touched[p.path]
		for _, d := range p.deps {
			reach = reach || touched[d]
		}
		for _, t := range p.tests {
			reach = reach || touched[t]
			for _, d := range byPath[t].deps {
				reach = reach || touched[d]
			}
		}
		if reach {
			out = append(out, p.path)
		}
	}
	sort.Strings(out)
	return out
}
