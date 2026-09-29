package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/xmonader/janetcode/internal/headless"
	"github.com/xmonader/janetcode/internal/mcpserve"
	"github.com/xmonader/janetcode/sdk"
)

// mcpServeMode serves janetcode as an MCP server on stdio (--mcp-serve).
// Each task runs in a janetcode child speaking ACP, started once and
// shared, so a task gets the whole agent — the same one --acp and the
// SDK give — under the permission mode chosen here. Read-only unless a
// mode flag says otherwise: a server's caller is another program, and
// nobody is there to answer what a mode would ask, so it is refused.
func mcpServeMode(o *options) error {
	if o.promptText != "" || o.acpMode || o.web || o.bg {
		return headless.BadInput("--mcp-serve runs on its own; it does not combine with -p, --acp, --web or --bg")
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	mode, label := sdk.ModeReadOnly, "read-only"
	switch {
	case o.yolo:
		mode, label = sdk.ModeYolo, "yolo: everything runs"
	case o.autoMode:
		mode, label = sdk.ModeAuto, "auto: in-workspace edits and safe commands run"
	case o.autoEdits:
		mode, label = sdk.ModeAutoEdits, "auto-edits: file edits run, commands are refused"
	}
	var args []string
	if o.effort != "" {
		args = append(args, "--effort", o.effort)
	}
	if o.sandboxOn {
		args = append(args, "--sandbox")
	}
	if o.allow != "" {
		args = append(args, "--allow", o.allow)
	}
	if o.deny != "" {
		args = append(args, "--deny", o.deny)
	}
	if o.appendSystem != "" {
		args = append(args, "--append-system-prompt", o.appendSystem)
	}
	for _, d := range o.addDir {
		args = append(args, "--add-dir", d)
	}
	for _, s := range o.sets {
		args = append(args, "--set", s)
	}
	r := &acpTaskRunner{opts: sdk.Options{
		Binary: self, Dir: cwd, Model: o.model, Provider: o.provider, Mode: mode,
		Args: args, Stderr: os.Stderr,
	}}
	defer r.close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fmt.Fprintf(os.Stderr, "janetcode %s serving MCP on stdio in %s (%s)\n", version, cwd, label)
	return mcpserve.Serve(ctx, os.Stdin, os.Stdout, mcpserve.Options{
		Runner: r, Version: version, Mode: label, ReadOnly: mode == sdk.ModeReadOnly, Dir: cwd,
	})
}

// acpTaskRunner runs MCP tasks in one janetcode ACP child, one session
// per conversation.
type acpTaskRunner struct {
	opts sdk.Options

	mu       sync.Mutex
	client   *sdk.Client
	sessions map[string]*sdk.Session
	denied   map[string]int // session id → calls the mode refused
}

func (r *acpTaskRunner) start(ctx context.Context) (*sdk.Client, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.client != nil {
		return r.client, nil
	}
	o := r.opts
	o.Approve = func(_ context.Context, p sdk.Permission) sdk.Decision {
		r.mu.Lock()
		r.denied[p.SessionID]++
		r.mu.Unlock()
		return sdk.Deny
	}
	c, err := sdk.Start(ctx, o)
	if err != nil {
		return nil, err
	}
	r.client = c
	r.sessions = map[string]*sdk.Session{}
	r.denied = map[string]int{}
	return c, nil
}

func (r *acpTaskRunner) session(ctx context.Context, c *sdk.Client, id string) (*sdk.Session, error) {
	r.mu.Lock()
	s := r.sessions[id]
	r.mu.Unlock()
	if s != nil {
		return s, nil
	}
	var err error
	if id == "" {
		s, err = c.NewSession(ctx)
	} else {
		s, err = c.LoadSession(ctx, id)
	}
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.sessions[s.ID] = s
	r.mu.Unlock()
	return s, nil
}

func (r *acpTaskRunner) Run(ctx context.Context, t mcpserve.Task, progress func(string)) (mcpserve.Report, error) {
	c, err := r.start(context.WithoutCancel(ctx))
	if err != nil {
		return mcpserve.Report{}, err
	}
	s, err := r.session(ctx, c, t.SessionID)
	if err != nil {
		return mcpserve.Report{}, err
	}
	r.mu.Lock()
	before := r.denied[s.ID]
	r.mu.Unlock()
	var onEvent func(sdk.Event)
	if progress != nil {
		onEvent = func(e sdk.Event) {
			if e.Kind == sdk.EventToolCall && e.Tool != nil {
				progress(e.Tool.Title)
			}
		}
	}
	res, err := s.PromptWith(ctx, t.Prompt, onEvent)
	if err != nil {
		return mcpserve.Report{}, err
	}
	r.mu.Lock()
	denied := r.denied[s.ID] - before
	r.mu.Unlock()
	return mcpserve.Report{
		SessionID: res.SessionID, Text: res.Text, StopReason: res.StopReason, Verdict: res.Verdict,
		ToolCalls: len(res.ToolCalls), Denied: denied,
		InputTokens: res.Usage.InputTokens, OutputTokens: res.Usage.OutputTokens, CostUSD: res.Usage.CostUSD,
	}, nil
}

func (r *acpTaskRunner) close() {
	r.mu.Lock()
	c := r.client
	r.mu.Unlock()
	if c != nil {
		c.Close()
	}
}
