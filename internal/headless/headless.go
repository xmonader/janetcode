// Package headless runs one prompt without a TUI, for scripting and CI.
package headless

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/gitx"
	"github.com/xmonader/janetcode/internal/i18n"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/memory"
	"github.com/xmonader/janetcode/internal/present"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/shell"
	"github.com/xmonader/janetcode/internal/sleepfold"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// Options configures a headless run.
type Options struct {
	Agent   *agent.Agent
	Session *session.Session
	Prompt  string
	// Images are sent with the prompt as image blocks.
	Images []llm.Block
	Stdout io.Writer
	Stderr io.Writer
	// Stdin is used for permission prompts when it is a terminal.
	Stdin *os.File
	// Until is a shell command that must exit 0 for the run to be done.
	// After each turn it is run in Cwd; on failure its output goes back to
	// the model and another turn starts, up to UntilRounds times.
	Until       string
	UntilRounds int
	// Memory, when set with AutoMemory, receives facts extracted after
	// each turn that changed something.
	Memory     *memory.Store
	AutoMemory bool
	Cwd        string
	// Watch is a shell command re-run whenever files under Cwd change; a
	// failure is sent to the model to fix. Run returns only when ctx ends.
	Watch string
	// WatchInterval is the poll interval; zero means 1s.
	WatchInterval time.Duration
	// WatchRounds bounds consecutive fix attempts per failure; zero means 5.
	WatchRounds int
	// Format is how the run reports itself: text, json or stream-json.
	// The zero value is text.
	Format Format
	// MaxCostUSD ends the run once it has spent this much. The agent
	// enforces it within a turn, after every model call; this is the
	// between-turns backstop for the rounds --until and --watch drive.
	// Zero means no ceiling.
	MaxCostUSD float64

	// printer, when set, renders each event in text mode instead of
	// printEvent: the screen-reader conversation's labelled lines.
	printer func(agent.Event)
	// conversing marks a turn that is one of many in a conversation
	// (Converse): the session does not end with it, so the end-of-session
	// work waits for the conversation's end.
	conversing bool
}

// DefaultUntilRounds bounds the re-loops when --until keeps failing.
const DefaultUntilRounds = 5

// ErrUntilFailed reports that the check command never passed.
var ErrUntilFailed = errors.New("the --until check did not pass")

// Run executes the prompt, streaming assistant text to Stdout and tool
// activity to Stderr. It returns the agent's error, if any.
func Run(ctx context.Context, opts Options) error {
	format := opts.Format
	if format == "" {
		format = FormatText
	}
	rec := newRecorder(opts, format)
	err := run(ctx, opts, rec)
	rec.finish(opts.Agent, err)
	return err
}

func run(ctx context.Context, opts Options, rec *recorder) error {
	ag := opts.Agent
	if ag.Approver == nil {
		// Ours to own: the approver holds a goroutine reading Stdin, so it
		// is closed and unset again on the way out rather than left on the
		// agent, where a later run would find a closed approver that denies
		// everything.
		sa := newStdinApprover(opts.Stdin, opts.Stderr)
		defer func() {
			_ = sa.Close()
			ag.Approver = nil
		}()
		ag.Approver = sa
	}
	if opts.MaxCostUSD > 0 {
		ag.MaxCostUSD = opts.MaxCostUSD
	}
	if opts.Watch != "" {
		return watch(ctx, opts, rec)
	}
	blocks := append([]llm.Block{{Type: llm.BlockText, Text: opts.Prompt}}, opts.Images...)
	rounds := opts.UntilRounds
	if rounds <= 0 {
		rounds = DefaultUntilRounds
	}
	for round := 1; ; round++ {
		if err := turn(ctx, opts, rec, blocks); err != nil {
			return err
		}
		if opts.MaxCostUSD > 0 {
			if spent, ok := rec.spentUSD(); ok && spent >= opts.MaxCostUSD {
				return fmt.Errorf("%w: spent %s of %s", ErrBudget, llm.FormatUSDPrecise(spent), llm.FormatUSDPrecise(opts.MaxCostUSD))
			}
		}
		if opts.Until == "" {
			return nil
		}
		out, exit := check(ctx, opts.Cwd, opts.Until)
		if err := ctx.Err(); err != nil {
			// The interrupt killed the check; it did not fail. Reading
			// it as a failed round exited 1 ("did not pass") instead of
			// 130, or started another turn to fix a check nobody broke.
			return err
		}
		if exit == 0 {
			// The turn's verdict was printed before this check ran and may
			// have said "unverified"; the check that just passed is the
			// verification, so the last word says so.
			fmt.Fprintf(opts.Stderr, "[until] passed: %s · this verifies the run\n", opts.Until)
			return nil
		}
		fmt.Fprintf(opts.Stderr, "[until] round %d/%d failed (exit %d): %s\n", round, rounds, exit, safeLine(out, 200))
		if round >= rounds {
			return fmt.Errorf("%w after %d rounds: %s", ErrUntilFailed, rounds, safeLine(out, 300))
		}
		blocks = []llm.Block{{Type: llm.BlockText, Synthetic: true, Text: fmt.Sprintf("The check command has not passed yet.\n\nCommand: %s\nExit code: %d\nOutput:\n```\n%s\n```\n\nKeep working until this command exits 0, then stop.", opts.Until, exit, textfmt.TruncateTo(strings.TrimSpace(out), 4000))}}
	}
}

// watch re-runs the watched command whenever the tree changes and asks
// the model to fix failures. It ends when ctx does.
func watch(ctx context.Context, opts Options, rec *recorder) error {
	interval := opts.WatchInterval
	if interval <= 0 {
		interval = time.Second
	}
	rounds := opts.WatchRounds
	if rounds <= 0 {
		rounds = DefaultUntilRounds
	}
	fmt.Fprintf(opts.Stderr, "[watch] %s · runs when files under %s change · ctrl+c stops\n", opts.Watch, opts.Cwd)
	last := ""
	fixes := 0
	for {
		fp := gitx.TreeFingerprint(opts.Cwd)
		if fp == last {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(interval):
			}
			continue
		}
		last = fp
		out, exit := check(ctx, opts.Cwd, opts.Watch)
		if ctx.Err() != nil {
			return nil
		}
		if exit == 0 {
			fmt.Fprintf(opts.Stderr, "[watch] ✓ %s\n", opts.Watch)
			fixes = 0
			continue
		}
		fmt.Fprintf(opts.Stderr, "[watch] ✗ exit %d: %s\n", exit, safeLine(out, 200))
		if fixes >= rounds {
			fmt.Fprintf(opts.Stderr, "[watch] %d fixes did not make it pass; waiting for your change\n", fixes)
			continue
		}
		fixes++
		var sb strings.Builder
		if opts.Prompt != "" && len(opts.Agent.Messages) == 0 {
			sb.WriteString(opts.Prompt + "\n\n")
		}
		fmt.Fprintf(&sb, "The watched command failed after files changed.\n\nCommand: %s\nExit code: %d\nOutput:\n```\n%s\n```\n\nFind the cause and fix it (fix attempt %d of %d). Do not weaken the check itself.", opts.Watch, exit, textfmt.TruncateTo(strings.TrimSpace(out), 4000), fixes, rounds)
		fmt.Fprintf(opts.Stderr, "[watch] asking the model to fix it (%d/%d)\n", fixes, rounds)
		if err := turn(ctx, opts, rec, []llm.Block{{Type: llm.BlockText, Synthetic: true, Text: sb.String()}}); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if errors.Is(err, ErrBudget) {
				// Reaching the ceiling is not a fix-round failure to
				// retry: every new round burns a full priced call
				// before the in-turn stop, so the loop would never
				// end — the run must exit on the budget instead.
				return err
			}
			fmt.Fprintf(opts.Stderr, "[watch] model turn failed: %v\n", err)
		}
		if opts.MaxCostUSD > 0 {
			if spent, ok := rec.spentUSD(); ok && spent >= opts.MaxCostUSD {
				return fmt.Errorf("%w: spent %s of %s", ErrBudget, llm.FormatUSDPrecise(spent), llm.FormatUSDPrecise(opts.MaxCostUSD))
			}
		}
		last = "" // re-check right away: the fix changed the tree
	}
}

// remember extracts durable facts from the turn into project memory.
func remember(ctx context.Context, opts Options) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	ag := opts.Agent
	xs, err := memory.Extract(ctx, ag.Provider, ag.Model, ag.Messages, opts.Memory.Entries())
	if err != nil {
		fmt.Fprintf(opts.Stderr, "[memory] extraction failed: %v\n", err)
		return
	}
	if s := opts.Session; s != nil {
		if ev := s.Events(); len(ev) > 0 {
			xs = memory.StampEvidence(xs, fmt.Sprintf("%s#~%d", s.Meta.ID, ev[len(ev)-1].Seq))
		}
	}
	changed, err := opts.Memory.Apply(xs)
	if err != nil {
		fmt.Fprintf(opts.Stderr, "[memory] %v\n", err)
	}
	for _, e := range changed {
		fmt.Fprintf(opts.Stderr, "[memory] remembered %s (%s, %s): %s\n", e.Name, e.Type, e.Scope, e.Description)
	}
}

// sleepSession consolidates what the session's record established into
// memory, each entry citing the record lines it stands on.
func sleepSession(ctx context.Context, opts Options) {
	sess := opts.Session
	if sess == nil {
		return // no ledger, no record to read
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	in := sleepfold.Fold(sess.Events())
	in.SessionID = sess.Meta.ID
	xs, dropped, err := memory.SleepD(ctx, opts.Agent.Provider, opts.Agent.Model, in, opts.Memory.Entries())
	if err != nil {
		fmt.Fprintf(opts.Stderr, "[memory] sleep failed: %v\n", err)
		return
	}
	changed, err := opts.Memory.ApplyWith(xs, "sleep")
	if err != nil {
		fmt.Fprintf(opts.Stderr, "[memory] %v\n", err)
	}
	// Nothing kept is the usual outcome and not news; it ended every
	// headless run with a line about an internal pass.
	_ = dropped
	for _, e := range changed {
		fmt.Fprintf(opts.Stderr, "[memory] sleep remembered %s: %s\n", e.Name, e.Description)
	}
}

// check runs the --until command and returns its output and exit code.
func check(ctx context.Context, cwd, command string) (string, int) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	cmd := shell.Command(ctx, command)
	// Supervise makes the ten-minute bound real: without a process group
	// the context kills only the shell, and a grandchild holding the
	// output pipes (a watch whose `npm test` leaks a server) blocks Wait
	// forever — the check, and with it the whole --until/--watch loop,
	// ignored both the timeout and ctrl+c.
	shell.Supervise(cmd)
	cmd.Dir = cwd
	out, err := cmd.CombinedOutput()
	// The command itself succeeded (exit 0) but left something running
	// that held its pipes: the supervise's bounded pipe wait cut the
	// read early. That is not a failed check.
	if err == nil || errors.Is(err, exec.ErrWaitDelay) {
		return string(out), 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return string(out), ee.ExitCode()
	}
	return string(out) + err.Error(), 1
}

// turn runs one agent turn. The session file is written as the turn
// happens, by the agent's event log.
func turn(ctx context.Context, opts Options, rec *recorder, blocks []llm.Block) error {
	ag := opts.Agent
	if ag.Log == nil && opts.Session != nil {
		ag.Log = opts.Session
	}
	err := ag.RunBlocks(ctx, blocks, func(e agent.Event) {
		rec.event(e)
		if !rec.text() {
			// json collects and prints once at the end; stream-json has
			// already written this event as a line of its own.
			return
		}
		if opts.printer != nil {
			opts.printer(e)
			return
		}
		printEvent(opts, e)
	})
	if err == nil && opts.Memory != nil && opts.AutoMemory && (ag.LastTurnMutated() || memory.LooksLikeFeedback(llm.Message{Blocks: blocks}.Text())) {
		remember(ctx, opts)
	}
	if !opts.conversing {
		// A headless run is one session per process, so its end is the
		// session's end.
		endSession(ctx, opts, err == nil && ag.LastTurnMutated())
	}
	// The turn is already on disk: every message was appended to the log as
	// it happened. Only the title still needs writing.
	if opts.Session != nil && opts.Session.Meta.Title == "" {
		if t := session.AutoTitle(ag.Messages); t != "" {
			if serr := opts.Session.SetTitle(t); serr != nil {
				fmt.Fprintf(opts.Stderr, "[warn] could not save session title: %v\n", serr)
			}
		}
	}
	return explainFailure(err, opts)
}

// endSession is the work a session's end owes. The outcome classification
// (item 9) runs first and writes its answer into the record, so
// consolidation reads a record that says how the session ended; with the
// consumer off it does nothing at all. mutated says whether the session
// changed anything worth consolidating.
func endSession(ctx context.Context, opts Options, mutated bool) {
	ag := opts.Agent
	ag.ClassifySessionOutcome(ctx)
	if mutated && opts.Memory != nil && opts.AutoMemory && !ag.Off["sleep"] {
		sleepSession(ctx, opts)
	}
}

// stdinApprover asks on the terminal; without a terminal it denies, so a
// CI run never silently mutates anything unless --yolo was given.
//
// One goroutine owns the input for the approver's life and hands each
// line to whichever prompt is waiting. A prompt abandoned by a cancelled
// context therefore leaves nothing behind to swallow the next answer, and
// no read-ahead is lost to a throwaway bufio.Reader.
type stdinApprover struct {
	in  *os.File
	out io.Writer
	// src is what answers are read from; in when nil. Tests set it with
	// interactive, since a pipe is not a terminal.
	src         io.Reader
	interactive func() bool
	// screenReader words the question for the line interface: the state
	// in words, then the call, then what to type (askSpoken).
	screenReader bool

	// asking admits one prompt at a time. Parallel subagents that bubble
	// their approvals, or a background fork beside the main agent, ask
	// at once; with both prompts printed, the line typed went to
	// whichever happened to be waiting, so a "y" meant for one call
	// could approve the other.
	asking chan struct{}

	start     sync.Once
	lines     chan lineRead
	done      chan struct{}
	closeOnce sync.Once
}

type lineRead struct {
	s   string
	err error
}

// ClosableApprover is an approver that owns something -- here, the
// goroutine reading the terminal -- and so has to be released. It is what
// NewStdinApprover returns, because an approver whose reader cannot be
// stopped leaks that goroutine and keeps stdin claimed for the life of
// the process.
type ClosableApprover interface {
	agent.Approver
	io.Closer
}

// NewStdinApprover is the approver Run installs when none is set. It is
// exported so another headless entry point — `janetcode --recipe`, which
// runs Agent.RunRecipe directly rather than through Run — can give a
// replayed step the same permission prompt a live -p run would. The
// caller owns the returned approver and must Close it: until then a
// goroutine is blocked reading in.
func NewStdinApprover(in *os.File, out io.Writer) ClosableApprover {
	return newStdinApprover(in, out)
}

func newStdinApprover(in *os.File, out io.Writer) *stdinApprover {
	return &stdinApprover{in: in, out: out, asking: make(chan struct{}, 1), lines: make(chan lineRead), done: make(chan struct{})}
}

// Close stops the line reader. A prompt after Close is denied.
func (a *stdinApprover) Close() error {
	a.closeOnce.Do(func() { close(a.done) })
	return nil
}

func (a *stdinApprover) canAsk() bool {
	if a.interactive != nil {
		return a.interactive()
	}
	return a.in != nil && isTerminal(a.in)
}

// readLines is the single reader. After a read error it keeps offering
// that error, so every later prompt sees the closed input as before.
func (a *stdinApprover) readLines() {
	src := a.src
	if src == nil {
		src = a.in
	}
	br := bufio.NewReader(src)
	for {
		line, err := br.ReadString('\n')
		r := lineRead{line, err}
		for {
			select {
			case a.lines <- r:
			case <-a.done:
				return
			}
			if err == nil {
				break
			}
		}
	}
}

func (a *stdinApprover) Approve(ctx context.Context, call agent.ToolCall) (agent.Decision, error) {
	d, _, err := a.ApproveWithReason(ctx, call)
	return d, err
}

// ApproveWithReason reads one line: y, a, or n optionally followed by a
// reason for the model ("n use the config dir instead").
func (a *stdinApprover) ApproveWithReason(ctx context.Context, call agent.ToolCall) (agent.Decision, string, error) {
	if !a.canAsk() {
		fmt.Fprintf(a.out, "[permission] %s denied: stdin is not a terminal (use --yolo to auto-approve)\n", call.Name)
		return agent.Deny, "", nil
	}
	select {
	case a.asking <- struct{}{}:
		defer func() { <-a.asking }()
	case <-ctx.Done():
		return agent.Deny, "", ctx.Err()
	case <-a.done:
		return agent.Deny, "", nil
	}
	if a.screenReader {
		a.askSpoken(call)
	} else {
		if call.Preview != "" {
			fmt.Fprintf(a.out, "\n%s\n", strings.TrimRight(call.Preview, "\n"))
		}
		if call.Warning != "" {
			fmt.Fprintf(a.out, "\nWARNING: %s\n", call.Warning)
		}
		rule := ""
		if call.Rule != "" {
			rule = fmt.Sprintf(" / [r]ule %s saved for good", call.Rule)
		}
		fmt.Fprintf(a.out, "\nAllow %s %s? [y]es / [a]lways this session%s / [n]o (add a reason after n): ", call.Name, textfmt.Clip(textfmt.OneLine(string(call.Input)), 200), rule)
	}
	s, err := a.nextLine(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return agent.Deny, "", ctx.Err()
		}
		return agent.Deny, "", nil
	}
	d, reason := parseAnswer(s)
	if a.screenReader {
		if d == agent.Deny {
			fmt.Fprintln(a.out, i18n.T("Denied."))
		} else {
			fmt.Fprintln(a.out, i18n.T("Allowed."))
		}
	}
	return d, reason, nil
}

// parseAnswer reads y, a, r or n, the last optionally followed by a
// reason for the model ("n use the config dir instead"). Anything else
// is a no.
func parseAnswer(s string) (agent.Decision, string) {
	word, rest, _ := strings.Cut(strings.TrimSpace(s), " ")
	switch strings.ToLower(word) {
	case "y", "yes":
		return agent.Allow, ""
	case "a", "always":
		return agent.AllowSession, ""
	case "r", "rule":
		return agent.AllowAlways, ""
	}
	return agent.Deny, strings.TrimSpace(rest)
}

// askSpoken is the permission question for a screen reader: the state
// named first, in words, then what the call would do, then what to type
// — each on a line of its own, nothing drawn and nothing overwritten.
func (a *stdinApprover) askSpoken(call agent.ToolCall) {
	what := call.Name
	if in := textfmt.Clip(textfmt.OneLine(string(call.Input)), 200); in != "" {
		what += " " + in
	}
	fmt.Fprintln(a.out, i18n.Tf("Waiting for permission: %s.", safe(what)))
	if call.Warning != "" {
		fmt.Fprintln(a.out, "WARNING: "+safe(call.Warning))
	}
	if call.Preview != "" {
		fmt.Fprintln(a.out, readable(safe(strings.TrimRight(call.Preview, "\n"))))
	}
	if call.Rule != "" {
		fmt.Fprintln(a.out, i18n.Tf("Type y to allow once, a to allow for this session, r to save the rule %s, n to deny. You can add a reason after n.", call.Rule))
	} else {
		fmt.Fprintln(a.out, i18n.T("Type y to allow once, a to allow for this session, n to deny. You can add a reason after n."))
	}
	fmt.Fprint(a.out, "> ")
}

// nextLine is the next line typed, read by the approver's one reader so a
// conversation's prompts and its permission answers never race for the
// same input.
func (a *stdinApprover) nextLine(ctx context.Context) (string, error) {
	a.start.Do(func() { go a.readLines() })
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-a.done:
		return "", io.EOF
	case r := <-a.lines:
		if r.err != nil && r.s == "" {
			return "", r.err
		}
		return strings.TrimRight(r.s, "\r\n"), nil
	}
}

func isTerminal(f *os.File) bool {
	return term.IsTerminal(int(f.Fd()))
}

// StdinInteractive reports whether f can actually answer a permission
// prompt: non-nil and a terminal. Exported so another headless entry
// point that has to decide *before* starting whether it could ever ask —
// `janetcode --recipe`'s preflight check, which fails fast rather than
// let every mutating step auto-deny one at a time — can ask the same
// question stdinApprover answers for itself on each call.
func StdinInteractive(f *os.File) bool {
	return f != nil && isTerminal(f)
}

// safe is what text bound for a terminal goes through. Model prose, tool
// output, an MCP server's tool name, and a watched command's stderr all
// reach these prints, and any of them can carry an escape sequence that
// repaints the caller's terminal or reads its clipboard back. Naming it
// once is the point: the escapes that got through were the call sites
// that spelled the stripping out and the ones that forgot to.
func safe(s string) string { return textfmt.StripUnsafeANSI(s) }

// safeLine is safe, flattened to one line and clipped to n columns.
func safeLine(s string, n int) string { return textfmt.Clip(textfmt.OneLine(safe(s)), n) }

// printEvent writes one event in text mode. It is a named function so
// the stripping every branch owes its output can be tested per branch:
// the branches that carried an escape through were the ones no test
// reached.
func printEvent(opts Options, e agent.Event) {
	switch e.Kind {
	case agent.EvText:
		fmt.Fprint(opts.Stdout, safe(e.Text))
	case agent.EvToolRun:
		// The model's own reason for the call, when it gave one, reads
		// better than its arguments.
		if e.Call.Intent != "" {
			fmt.Fprintf(opts.Stderr, "\n[tool] %s — %s\n       %s\n", safe(e.Call.Name), safe(e.Call.Intent), safeLine(string(e.Call.Input), 160))
		} else {
			fmt.Fprintf(opts.Stderr, "\n[tool] %s %s\n", safe(e.Call.Name), safeLine(string(e.Call.Input), 200))
		}
	case agent.EvToolResult:
		tag := "ok"
		if e.IsError {
			tag = "error"
		}
		fmt.Fprintf(opts.Stderr, "[tool %s] %s\n", tag, safeLine(e.Output, 300))
	case agent.EvCompacted:
		fmt.Fprintf(opts.Stderr, "[compacted] %s\n", safe(e.Text))
	case agent.EvVerdict:
		// What the turn demonstrated. It is the point of watching a
		// run at all, so it goes to stderr beside the tool activity
		// rather than being dropped as the TUI-only line it was.
		fmt.Fprintf(opts.Stderr, "\n[verdict] %s\n", safe(e.Text))
	case agent.EvRetry:
		fmt.Fprintf(opts.Stderr, "[retry] %s\n", safe(e.Text))
	case agent.EvDiscard:
		// The dead attempt's stdout text is void, and bytes already
		// written to a pipe cannot be recalled: the newline keeps the
		// retried answer from running into it and the notice says
		// which half to read. The accumulated answer the json modes
		// report is reset properly, in recorder.event.
		fmt.Fprintln(opts.Stdout)
		fmt.Fprintln(opts.Stderr, "[discarded] the answer so far was cut off mid-stream and is being resent; the text above it is void")
	case agent.EvNotice:
		if !present.Routine(e.Text) {
			fmt.Fprintf(opts.Stderr, "[notice] %s\n", safe(e.Text))
		}
	case agent.EvTurnDone:
		fmt.Fprintln(opts.Stdout)
		fmt.Fprintf(opts.Stderr, "[usage] in=%d out=%d cache_read=%d stop=%s", e.Usage.InputTokens, e.Usage.OutputTokens, e.Usage.CacheReadTokens, e.StopReason)
		if usd, ok := e.Usage.Cost(opts.Agent.Price); ok {
			fmt.Fprintf(opts.Stderr, " cost=%s", llm.FormatUSD(usd))
		}
		fmt.Fprintln(opts.Stderr)
	}
}

// cliRemedies are the fixes a command line can reach for.
var cliRemedies = llm.Remedies{Key: "(janetcode --doctor checks it)", Model: "with --model (janetcode --inspect providers shows the endpoint)"}

// explainedError is a provider failure in the words the terminal UI
// uses for it, still unwrapping to the original so the exit status and
// KeyRejected read the same cause.
type explainedError struct {
	msg string
	err error
}

func (e explainedError) Error() string { return e.msg }
func (e explainedError) Unwrap() error { return e.err }

// explainFailure turns a raw provider failure ("Post \"...\": dial tcp
// ...: connection refused") into what failed and what to try. Errors
// it does not recognise, and the run's own stops (cancelled, budget,
// turn limit), pass through unchanged.
func explainFailure(err error, opts Options) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, ErrBudget) || errors.Is(err, agent.ErrMaxIterations) {
		return err
	}
	provider := "the provider"
	if opts.Session != nil && opts.Session.Meta.Provider != "" {
		provider = opts.Session.Meta.Provider
	}
	x := llm.Explain(err, provider, cliRemedies)
	if x.Headline == "" {
		return err
	}
	return explainedError{msg: x.String(), err: err}
}

// Explained reports whether err already carries its own next step, so a
// caller does not add a second one.
func Explained(err error) bool {
	var x explainedError
	return errors.As(err, &x)
}
