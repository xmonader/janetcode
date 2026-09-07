// Package headless runs one prompt without a TUI, for scripting and CI.
package headless

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/session"
)

// Options configures a headless run.
type Options struct {
	Agent   *agent.Agent
	Session *session.Session
	Prompt  string
	Stdout  io.Writer
	Stderr  io.Writer
	// Stdin is used for permission prompts when it is a terminal.
	Stdin *os.File
}

// Run executes the prompt, streaming assistant text to Stdout and tool
// activity to Stderr. It returns the agent's error, if any.
func Run(ctx context.Context, opts Options) error {
	ag := opts.Agent
	if ag.Approver == nil {
		ag.Approver = &stdinApprover{in: opts.Stdin, out: opts.Stderr}
	}
	err := ag.Run(ctx, opts.Prompt, func(e agent.Event) {
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
		case agent.EvTurnDone:
			fmt.Fprintln(opts.Stdout)
			fmt.Fprintf(opts.Stderr, "[usage] in=%d out=%d cache_read=%d stop=%s\n", e.Usage.InputTokens, e.Usage.OutputTokens, e.Usage.CacheReadTokens, e.StopReason)
		}
	})
	if opts.Session != nil {
		if serr := opts.Session.Sync(ag.Messages); serr != nil {
			fmt.Fprintf(opts.Stderr, "[warn] could not save session: %v\n", serr)
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
	if a.in == nil || !isTerminal(a.in) {
		fmt.Fprintf(a.out, "[permission] %s denied: stdin is not a terminal (use --yolo to auto-approve)\n", call.Name)
		return agent.Deny, nil
	}
	fmt.Fprintf(a.out, "\nAllow %s %s? [y]es / [a]lways / [n]o: ", call.Name, oneLine(string(call.Input), 200))
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
		return agent.Deny, ctx.Err()
	case r := <-ch:
		if r.err != nil && r.s == "" {
			return agent.Deny, nil
		}
		switch strings.ToLower(strings.TrimSpace(r.s)) {
		case "y", "yes":
			return agent.Allow, nil
		case "a", "always":
			return agent.AllowSession, nil
		}
		return agent.Deny, nil
	}
}

func isTerminal(f *os.File) bool {
	return term.IsTerminal(int(f.Fd()))
}
