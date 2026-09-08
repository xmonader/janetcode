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
	"time"

	"golang.org/x/term"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/memory"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
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
}

// DefaultUntilRounds bounds the re-loops when --until keeps failing.
const DefaultUntilRounds = 5

// ErrUntilFailed reports that the check command never passed.
var ErrUntilFailed = errors.New("the --until check did not pass")

// Run executes the prompt, streaming assistant text to Stdout and tool
// activity to Stderr. It returns the agent's error, if any.
func Run(ctx context.Context, opts Options) error {
	ag := opts.Agent
	if ag.Approver == nil {
		ag.Approver = &stdinApprover{in: opts.Stdin, out: opts.Stderr}
	}
	blocks := append([]llm.Block{{Type: llm.BlockText, Text: opts.Prompt}}, opts.Images...)
	rounds := opts.UntilRounds
	if rounds <= 0 {
		rounds = DefaultUntilRounds
	}
	for round := 1; ; round++ {
		if err := turn(ctx, opts, blocks); err != nil {
			return err
		}
		if opts.Until == "" {
			return nil
		}
		out, exit := check(ctx, opts.Cwd, opts.Until)
		if exit == 0 {
			fmt.Fprintf(opts.Stderr, "[until] passed: %s\n", opts.Until)
			return nil
		}
		fmt.Fprintf(opts.Stderr, "[until] round %d/%d failed (exit %d): %s\n", round, rounds, exit, oneLine(out, 200))
		if round >= rounds {
			return fmt.Errorf("%w after %d rounds: %s", ErrUntilFailed, rounds, oneLine(out, 300))
		}
		blocks = []llm.Block{{Type: llm.BlockText, Text: fmt.Sprintf("The check command has not passed yet.\n\nCommand: %s\nExit code: %d\nOutput:\n```\n%s\n```\n\nKeep working until this command exits 0, then stop.", opts.Until, exit, tool.TruncateTo(strings.TrimSpace(out), 4000))}}
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
	changed, err := opts.Memory.Apply(xs)
	if err != nil {
		fmt.Fprintf(opts.Stderr, "[memory] %v\n", err)
	}
	for _, e := range changed {
		fmt.Fprintf(opts.Stderr, "[memory] remembered %s (%s, %s): %s\n", e.Name, e.Type, e.Scope, e.Description)
	}
}

// check runs the --until command and returns its output and exit code.
func check(ctx context.Context, cwd, command string) (string, int) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	cmd.Dir = cwd
	out, err := cmd.CombinedOutput()
	if err == nil {
		return string(out), 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return string(out), ee.ExitCode()
	}
	return string(out) + err.Error(), 1
}

// turn runs one agent turn and syncs the session.
func turn(ctx context.Context, opts Options, blocks []llm.Block) error {
	ag := opts.Agent
	err := ag.RunBlocks(ctx, blocks, func(e agent.Event) {
		switch e.Kind {
		case agent.EvText:
			fmt.Fprint(opts.Stdout, e.Text)
		case agent.EvToolRun:
			fmt.Fprintf(opts.Stderr, "\n[tool] %s %s\n", e.Call.Name, oneLine(string(e.Call.Input), 200))
		case agent.EvToolResult:
			tag := "ok"
			if e.IsError {
				tag = "error"
			}
			fmt.Fprintf(opts.Stderr, "[tool %s] %s\n", tag, oneLine(e.Output, 300))
		case agent.EvCompacted:
			fmt.Fprintf(opts.Stderr, "[compacted] %s\n", e.Text)
		case agent.EvNotice:
			fmt.Fprintf(opts.Stderr, "[notice] %s\n", e.Text)
		case agent.EvTurnDone:
			fmt.Fprintln(opts.Stdout)
			fmt.Fprintf(opts.Stderr, "[usage] in=%d out=%d cache_read=%d stop=%s", e.Usage.InputTokens, e.Usage.OutputTokens, e.Usage.CacheReadTokens, e.StopReason)
			if usd, ok := e.Usage.Cost(opts.Agent.Price); ok {
				fmt.Fprintf(opts.Stderr, " cost=%s", llm.FormatUSD(usd))
			}
			fmt.Fprintln(opts.Stderr)
		}
	})
	if err == nil && opts.Memory != nil && opts.AutoMemory && (ag.LastTurnMutated() || memory.LooksLikeFeedback(llm.Message{Blocks: blocks}.Text())) {
		remember(ctx, opts)
	}
	if opts.Session != nil {
		ns, serr := opts.Session.SyncOrRotate(ag.Messages)
		if ns != opts.Session {
			ag.UndoPath = ns.UndoPath()
			ag.SaveUndo()
		}
		if serr != nil {
			fmt.Fprintf(opts.Stderr, "[warn] could not save session: %v\n", serr)
		} else if ns != opts.Session {
			fmt.Fprintf(opts.Stderr, "[session] context compacted; continuing in session %s\n", ns.Meta.ID)
		}
	}
	return err
}

func oneLine(s string, max int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

// stdinApprover asks on the terminal; without a terminal it denies, so a
// CI run never silently mutates anything unless --yolo was given.
type stdinApprover struct {
	in  *os.File
	out io.Writer
}

func (a *stdinApprover) Approve(ctx context.Context, call agent.ToolCall) (agent.Decision, error) {
	d, _, err := a.ApproveWithReason(ctx, call)
	return d, err
}

// ApproveWithReason reads one line: y, a, or n optionally followed by a
// reason for the model ("n use the config dir instead").
func (a *stdinApprover) ApproveWithReason(ctx context.Context, call agent.ToolCall) (agent.Decision, string, error) {
	if a.in == nil || !isTerminal(a.in) {
		fmt.Fprintf(a.out, "[permission] %s denied: stdin is not a terminal (use --yolo to auto-approve)\n", call.Name)
		return agent.Deny, "", nil
	}
	if call.Preview != "" {
		fmt.Fprintf(a.out, "\n%s\n", strings.TrimRight(call.Preview, "\n"))
	}
	if call.Warning != "" {
		fmt.Fprintf(a.out, "\nWARNING: %s\n", call.Warning)
	}
	fmt.Fprintf(a.out, "\nAllow %s %s? [y]es / [a]lways / [n]o (add a reason after n): ", call.Name, oneLine(string(call.Input), 200))
	type res struct {
		s   string
		err error
	}
	ch := make(chan res, 1)
	go func() {
		line, err := bufio.NewReader(a.in).ReadString('\n')
		ch <- res{line, err}
	}()
	select {
	case <-ctx.Done():
		return agent.Deny, "", ctx.Err()
	case r := <-ch:
		if r.err != nil && r.s == "" {
			return agent.Deny, "", nil
		}
		word, rest, _ := strings.Cut(strings.TrimSpace(r.s), " ")
		switch strings.ToLower(word) {
		case "y", "yes":
			return agent.Allow, "", nil
		case "a", "always":
			return agent.AllowSession, "", nil
		}
		return agent.Deny, strings.TrimSpace(rest), nil
	}
}

func isTerminal(f *os.File) bool {
	return term.IsTerminal(int(f.Fd()))
}
