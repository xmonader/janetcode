package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/perm"
	agentprofile "github.com/xmonader/janetcode/internal/skill/profile"
	"github.com/xmonader/janetcode/internal/tool"
)

// editingChild builds a subagent with the full tool set rooted at the
// worktree. It runs without an approver: nothing it does reaches the real
// tree, and the patch is reviewed before it is applied. Deny rules still
// hold.
func (t *Task) editingChild(wt *worktree, profile *agentprofile.Profile) (*Agent, error) {
	p := t.Parent
	// A child is built on the task goroutine while the parent's front-end
	// may be changing its settings; read them once, under their lock.
	ps := p.Settings()
	maxIter := t.MaxIterations
	if maxIter <= 0 {
		maxIter = 40
	}
	if profile != nil && profile.MaxTurns > 0 {
		maxIter = profile.MaxTurns
	}
	child, err := New(Deps{
		Provider: ps.Provider,
		Tools:    tool.NewRegistry(allowed(wt.tools(), profile)...),
	}, Settings{
		Model:         ps.Model,
		MaxTokens:     ps.MaxTokens,
		Effort:        ps.Effort,
		MaxIterations: maxIter,
		AutoApprove:   true,
		CompactAt:     ps.CompactAt,
		ContextWindow: ps.ContextWindow,
	})
	if err != nil {
		return nil, err
	}
	child.System = p.System + "\n\nYou are a subagent doing one delegated job for the main agent, in your own checkout of the project. Make the change requested, run the relevant build or tests, and finish with a short report of what you changed and how you verified it. Your edits are returned to the main agent as a patch; do not commit. Do not ask questions; make reasonable assumptions and state them."
	child.Rules = perm.Rules{Deny: p.RulesSnapshot().Deny}
	child.Ledger = p.Ledger
	child.Protected = p.Protected
	child.Impact = p.Impact
	// An editing subagent approves its own calls because nothing it does
	// reaches the real tree and the patch is reviewed before it is
	// applied. A profile can ask for the other arrangement: the
	// confirmation is queued to the parent's approver — the user — which
	// is the difference between a job that stops on a question and one
	// that quietly answers it for itself.
	if profile != nil && profile.Bubble() && p.Approver != nil {
		child.AutoApprove = false
		child.Approver = bubbled{to: p.Approver, name: profile.Name}
	}
	return child, nil
}

// bubbled sends a subagent's confirmation to the parent's approver,
// saying which subagent is asking: a prompt that does not say who wants
// the command is a prompt nobody can answer.
type bubbled struct {
	to   Approver
	name string
}

func (b bubbled) Approve(ctx context.Context, call ToolCall) (Decision, error) {
	if call.Intent == "" {
		call.Intent = "asked for by the " + b.name + " subagent"
	} else {
		call.Intent = "[" + b.name + " subagent] " + call.Intent
	}
	return b.to.Approve(ctx, call)
}

// allowed drops the tools a profile refuses. It runs after the
// allowlist, so a tool a profile denies cannot be let back in by naming
// it in tools.
func allowed(ts []tool.Tool, profile *agentprofile.Profile) []tool.Tool {
	if profile == nil || len(profile.Disallowed) == 0 {
		return ts
	}
	deny := map[string]bool{}
	for _, n := range profile.Disallowed {
		deny[strings.ToLower(strings.TrimSpace(n))] = true
	}
	out := make([]tool.Tool, 0, len(ts))
	for _, tl := range ts {
		if !deny[strings.ToLower(tl.Name())] {
			out = append(out, tl)
		}
	}
	return out
}

// parentOnlyTool reports tools that stay on the main session even when they
// are not mutating: they write the parent's logs, ask the user, or spawn
// more children through the parent's task tool.
func parentOnlyTool(tl tool.Tool) bool {
	switch tl.(type) {
	case *GoalStatus, *AskTool, *Workflow:
		return true
	}
	return false
}

// child builds the subagent: same provider and model, read-only tools only
// (or the named subset of them), no approver (a mutating tool would be
// denied outright).
func (t *Task) child(only []string, profile *agentprofile.Profile) (*Agent, error) {
	p := t.Parent
	// A child is built on the task goroutine while the parent's front-end
	// may be changing its settings; read them once, under their lock.
	ps := p.Settings()
	var ro []tool.Tool
	if len(only) > 0 {
		for _, name := range only {
			tl, ok := p.Tools.Get(name)
			if !ok {
				return nil, fmt.Errorf("unknown tool %q in tools", name)
			}
			if tl.Mutating() || name == t.Name() {
				return nil, fmt.Errorf("tool %q cannot be given to a subagent (read-only tools only)", name)
			}
			if parentOnlyTool(tl) {
				return nil, fmt.Errorf("tool %q cannot be given to a subagent", name)
			}
			ro = append(ro, tl)
		}
	} else {
		for _, d := range p.Tools.Defs() {
			tl, ok := p.Tools.Get(d.Name)
			if !ok || tl.Mutating() || d.Name == t.Name() || parentOnlyTool(tl) {
				continue
			}
			ro = append(ro, tl)
		}
	}
	maxIter := t.MaxIterations
	if maxIter <= 0 {
		maxIter = 25
	}
	if profile != nil && profile.MaxTurns > 0 {
		maxIter = profile.MaxTurns
	}
	if ro = allowed(ro, profile); len(ro) == 0 {
		return nil, fmt.Errorf("the %s agent has no tools left once its disallowed_tools are removed", profile.Name)
	}
	child, err := New(Deps{
		Provider: ps.Provider,
		Tools:    tool.NewRegistry(ro...),
	}, Settings{
		Model:         ps.Model,
		MaxTokens:     ps.MaxTokens,
		Effort:        ps.Effort,
		MaxIterations: maxIter,
		CompactAt:     ps.CompactAt,
		AutoApprove:   false,
	})
	if err != nil {
		return nil, err
	}
	child.System = p.System + "\n\nYou are a subagent working on one delegated job for the main agent. You cannot edit files or run commands. Investigate with the tools you have, then reply with a complete, self-contained report of what you found: relevant file paths with line numbers, the facts requested, and anything surprising. Do not ask questions; make reasonable assumptions and state them."
	child.Archive = p.Archive
	// A deny rule holds for a subagent too, as it does for the editing
	// one. Deny is consulted for read-only tools as well, so without this
	// a path the user refused could be read simply by delegating the job.
	// The allow list is deliberately not passed on: permission granted in
	// the session is not permission the subagent asked for.
	child.Rules = perm.Rules{Deny: p.RulesSnapshot().Deny}
	child.Debug = p.Debug
	child.Ledger = p.Ledger
	child.Protected = p.Protected
	child.Impact = p.Impact
	return child, nil
}

// forkChild builds a subagent that inherits the parent's conversation
// and tools (except task, ask, goal_status, and workflow). Approvals
// bubble to the user unless the parent is already auto-approving.
func (t *Task) forkChild() (*Agent, error) {
	p := t.Parent
	// A child is built on the task goroutine while the parent's front-end
	// may be changing its settings; read them once, under their lock.
	ps := p.Settings()
	var ts []tool.Tool
	for _, d := range p.Tools.Defs() {
		if d.Name == t.Name() {
			continue
		}
		tl, ok := p.Tools.Get(d.Name)
		if !ok {
			continue
		}
		if parentOnlyTool(tl) {
			continue
		}
		ts = append(ts, tl)
	}
	if len(ts) == 0 {
		return nil, fmt.Errorf("a fork has no tools left")
	}
	maxIter := t.MaxIterations
	if maxIter <= 0 {
		maxIter = 50
	}
	child, err := New(Deps{
		Provider: ps.Provider,
		Tools:    tool.NewRegistry(ts...),
	}, Settings{
		Model:         ps.Model,
		MaxTokens:     ps.MaxTokens,
		Effort:        ps.Effort,
		MaxIterations: maxIter,
		AutoApprove:   ps.AutoApprove,
		AutoEdits:     ps.AutoEdits,
		AutoMode:      ps.AutoMode,
		ReadOnly:      ps.ReadOnly,
		PlanMode:      ps.PlanMode,
		CompactAt:     ps.CompactAt,
		ContextWindow: ps.ContextWindow,
	})
	if err != nil {
		return nil, err
	}
	child.System = p.System + "\n\nYou are a fork of the parent conversation and inherited its history. Complete the brief. You cannot spawn another fork."
	// /agents fork starts this from a front-end goroutine while the
	// parent's turn may still be appending; copy under its lock.
	child.Messages = copyMessages(p.MessagesSnapshot())
	child.Rules = p.RulesSnapshot()
	child.Archive = p.Archive
	child.Debug = p.Debug
	child.Forked = true
	child.Ledger = p.Ledger
	child.Protected = p.Protected
	child.Impact = p.Impact
	if !ps.AutoApprove && p.Approver != nil {
		child.Approver = bubbled{to: p.Approver, name: "fork"}
	}
	return child, nil
}

func copyMessages(in []llm.Message) []llm.Message {
	out := make([]llm.Message, len(in))
	for i, m := range in {
		out[i] = m
		if m.Blocks == nil {
			continue
		}
		blocks := make([]llm.Block, len(m.Blocks))
		for j, b := range m.Blocks {
			blocks[j] = b
			if b.Input != nil {
				blocks[j].Input = append(json.RawMessage(nil), b.Input...)
			}
		}
		out[i].Blocks = blocks
	}
	return out
}
