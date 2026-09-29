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
		Tools:    tool.NewRegistry(allowed(wt.tools(p.Files), profile)...),
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
	child.Rules = childRules(p.RulesSnapshot())
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
	child.Files = p.Files
	withholdFromChild(child.Tools, profile)
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
		if profile == nil {
			return nil, fmt.Errorf("a subagent has no read-only tools to work with")
		}
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
		// The window is what sizes compaction and the oversized-output
		// guard; without it the child that reads the most code ran with
		// neither fitted to the model — compacting at a fixed 80k tokens
		// on a 32k model, and taking any file whole.
		ContextWindow: ps.ContextWindow,
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
	child.Rules = childRules(p.RulesSnapshot())
	child.Debug = p.Debug
	child.Ledger = p.Ledger
	child.Protected = p.Protected
	child.Impact = p.Impact
	child.Files = p.Files
	withholdFromChild(child.Tools, profile)
	return child, nil
}

func withholdFromChild(r *tool.Registry, profile *agentprofile.Profile) {
	if r == nil {
		return
	}
	for _, n := range []string{"ask", "task", "goal_status", "workflow"} {
		r.Withhold(n, "this tool stays on the parent session")
	}
	if profile == nil {
		return
	}
	for _, n := range profile.Disallowed {
		n = strings.TrimSpace(n)
		if n != "" {
			r.Withhold(n, "excluded by the "+profile.Name+" agent's disallowed_tools")
		}
	}
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
	child.Messages = closeOpenCalls(copyMessages(p.MessagesSnapshot()))
	child.Rules = p.RulesSnapshot()
	child.Archive = p.Archive
	child.Debug = p.Debug
	child.Forked = true
	// A fork writes into the session's own workspace, so its changes are
	// the session's changes: they belong in the parent's checkpoint, or
	// /undo cannot revert them.
	child.UndoOwner = p
	child.Ledger = p.Ledger
	child.Protected = p.Protected
	child.Impact = p.Impact
	child.Files = p.Files
	withholdFromChild(child.Tools, nil)
	if !ps.AutoApprove && p.Approver != nil {
		child.Approver = bubbled{to: p.Approver, name: "fork"}
	}
	return child, nil
}

// closeOpenCalls answers the tool calls a copied history ends on. A fork
// the model asks for is started from inside the parent's tool step: the
// assistant message carrying the task call is already in the history and
// its result is not, because that result is what the fork's start
// returns. Sent as it was, the fork's first request held a tool_use with
// no tool_result, which every provider refuses — so the fork failed on
// its first call. The parent's history is left as it is; it answers
// those calls itself when the step ends.
func closeOpenCalls(msgs []llm.Message) []llm.Message {
	n := len(msgs)
	if n == 0 || msgs[n-1].Role != llm.RoleAssistant {
		return msgs
	}
	uses := msgs[n-1].ToolUses()
	if len(uses) == 0 {
		return msgs
	}
	results := make([]llm.Block, 0, len(uses))
	for _, u := range uses {
		results = append(results, llm.ToolResult(replyID(u),
			u.Name+" was still running in the parent conversation when this fork started; its result is not part of the fork's history.", false))
	}
	return append(msgs, llm.ToolResultMessage(results...))
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

// childRules are the rules a subagent inherits: every deny and ask rule,
// so what the parent refuses or always asks about stays refused or asked
// about in a subagent, and no allow rule, since a subagent's approvals
// are its profile's.
func childRules(rs perm.Rules) perm.Rules {
	return perm.Rules{Deny: rs.Deny, Ask: rs.Ask}
}
