package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/tool"
)

// Task is a tool that runs a subagent: a fresh conversation with the same
// model and the parent's read-only tools, which explores or researches and
// returns a report. It never modifies files, so several can run at once.
type Task struct {
	Parent *Agent
	// MaxIterations bounds the child's tool rounds. Zero means 25.
	MaxIterations int

	mu    sync.Mutex
	usage llm.Usage
}

func (*Task) Name() string { return "task" }
func (*Task) Description() string {
	return "Delegate a self-contained read-only job to a subagent that has its own context window: exploring an unfamiliar area of the codebase, tracing how something works, or gathering facts across many files. It can read, list, glob, grep and fetch URLs but cannot edit files or run commands. Give it a complete brief with what to find and what to report back; it returns only its final report. Call several in one turn to work in parallel."
}
func (*Task) Mutating() bool { return false }
func (*Task) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
"description":{"type":"string","description":"Short label for what the subagent is doing (shown to the user)"},
"prompt":{"type":"string","description":"The full brief: goal, where to look, and exactly what to report"}},
"required":["description","prompt"],"additionalProperties":false}`)
}

// Usage is the total spent by subagents so far.
func (t *Task) Usage() llm.Usage {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.usage
}

func (t *Task) Run(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Description string `json:"description"`
		Prompt      string `json:"prompt"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("bad input: %w", err)
	}
	if strings.TrimSpace(in.Prompt) == "" {
		return "", fmt.Errorf("prompt must not be empty")
	}
	if t.Parent == nil {
		return "", fmt.Errorf("task tool has no parent agent")
	}
	child := t.child()
	start := time.Now()
	err := child.Run(ctx, in.Prompt, nil)
	t.mu.Lock()
	t.usage.Add(child.Usage)
	t.mu.Unlock()
	if err != nil {
		return "", fmt.Errorf("subagent failed after %s: %w", time.Since(start).Round(time.Millisecond), err)
	}
	report := lastAssistantText(child.Messages)
	if strings.TrimSpace(report) == "" {
		report = "(the subagent finished without a report)"
	}
	return fmt.Sprintf("%s\n\n[subagent %q: %d tool calls, %s, %s in / %s out]", report, in.Description, countToolUses(child.Messages), time.Since(start).Round(time.Millisecond), tokens(child.Usage.InputTokens+child.Usage.CacheReadTokens), tokens(child.Usage.OutputTokens)), nil
}

// child builds the subagent: same provider and model, read-only tools only,
// no approver (a mutating tool would be denied outright).
func (t *Task) child() *Agent {
	p := t.Parent
	var ro []tool.Tool
	for _, d := range p.Tools.Defs() {
		if tl, ok := p.Tools.Get(d.Name); ok && !tl.Mutating() && d.Name != t.Name() {
			ro = append(ro, tl)
		}
	}
	maxIter := t.MaxIterations
	if maxIter <= 0 {
		maxIter = 25
	}
	return &Agent{
		Provider:      p.Provider,
		Tools:         tool.NewRegistry(ro...),
		System:        p.System + "\n\nYou are a subagent working on one delegated job for the main agent. You cannot edit files or run commands. Investigate with the tools you have, then reply with a complete, self-contained report of what you found: relevant file paths with line numbers, the facts requested, and anything surprising. Do not ask questions; make reasonable assumptions and state them.",
		Model:         p.Model,
		MaxTokens:     p.MaxTokens,
		MaxIterations: maxIter,
		CompactAt:     p.CompactAt,
		Archive:       p.Archive,
		AutoApprove:   false,
		Debug:         p.Debug,
	}
}

func lastAssistantText(msgs []llm.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == llm.RoleAssistant {
			if t := msgs[i].Text(); t != "" {
				return t
			}
		}
	}
	return ""
}

func countToolUses(msgs []llm.Message) int {
	n := 0
	for _, m := range msgs {
		n += len(m.ToolUses())
	}
	return n
}

func tokens(n int) string {
	if n >= 1000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprintf("%d", n)
}
