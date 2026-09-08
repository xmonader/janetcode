package agent

import (
	"fmt"
	"sort"
	"strings"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/perm"
)

// systemPrompt is the full system text sent with a request: the base
// prompt, the dynamic extra (memory and skill indexes), pins and modes.
func (a *Agent) systemPrompt() string {
	system := a.System
	if a.SystemExtra != nil {
		system += a.SystemExtra()
	}
	if len(a.Pins) > 0 {
		var sb strings.Builder
		sb.WriteString("\n\n# Pinned by the user\nThese notes stay in context for the whole session, including after compaction.\n")
		for i, p := range a.Pins {
			fmt.Fprintf(&sb, "\n%d. %s\n", i+1, p)
		}
		system += sb.String()
	}
	if a.readOnly() {
		system += readOnlySystem
		if a.PlanMode {
			system += planSystem
		}
	}
	return system
}

// ContextReport breaks the context window down by what fills it.
type ContextReport struct {
	Total, CompactAt                                          int
	System, Extra, Pins, Tools, ToolCount, Deferred           int
	UserText, AssistantText, Thinking, ToolCalls, ToolResults int
	Images, Messages, Turns                                   int
	ByTool                                                    []ToolShare
	Largest                                                   []ContextItem
}

// ToolShare is one tool's calls and result size.
type ToolShare struct {
	Name          string
	Calls, Tokens int
}

// ContextItem is one large tool result.
type ContextItem struct {
	Index   int // message index
	Tool    string
	Subject string
	Tokens  int
}

// Context computes the report from the current history.
func (a *Agent) Context() ContextReport {
	r := ContextReport{CompactAt: a.CompactAt}
	if r.CompactAt == 0 {
		r.CompactAt = DefaultCompactAt
	}
	r.System = len(a.System) / 4
	if a.SystemExtra != nil {
		r.Extra = len(a.SystemExtra()) / 4
	}
	for _, p := range a.Pins {
		r.Pins += len(p)/4 + 4
	}
	for _, d := range a.Tools.Defs() {
		r.ToolCount++
		r.Tools += (len(d.Description) + len(d.InputSchema)) / 4
	}
	r.Deferred = len(a.Tools.Deferred())
	r.Messages = len(a.Messages)
	byTool := map[string]*ToolShare{}
	names := map[string]string{} // tool_use id → name
	subjects := map[string]string{}
	for i, m := range a.Messages {
		for _, b := range m.Blocks {
			switch b.Type {
			case llm.BlockText:
				n := len(b.Text) / 4
				if m.Role == llm.RoleUser {
					r.UserText += n
					r.Turns++
				} else {
					r.AssistantText += n
				}
			case llm.BlockThinking:
				r.Thinking += len(b.Text) / 4
			case llm.BlockImage:
				r.Images++
			case llm.BlockToolUse:
				n := (len(b.Input) + len(b.Name) + 16) / 4
				r.ToolCalls += n
				names[b.ID] = b.Name
				subjects[b.ID] = perm.Subject(b.Name, b.Input)
				s := byTool[b.Name]
				if s == nil {
					s = &ToolShare{Name: b.Name}
					byTool[b.Name] = s
				}
				s.Calls++
				s.Tokens += n
			case llm.BlockToolResult:
				n := len(b.Text)/4 + 4
				r.ToolResults += n
				name := names[b.ToolUseID]
				if name == "" {
					name = "(unknown)"
				}
				s := byTool[name]
				if s == nil {
					s = &ToolShare{Name: name}
					byTool[name] = s
				}
				s.Tokens += n
				r.Largest = append(r.Largest, ContextItem{Index: i, Tool: name, Subject: subjects[b.ToolUseID], Tokens: n})
			}
		}
	}
	for _, s := range byTool {
		r.ByTool = append(r.ByTool, *s)
	}
	sort.Slice(r.ByTool, func(i, j int) bool { return r.ByTool[i].Tokens > r.ByTool[j].Tokens })
	sort.Slice(r.Largest, func(i, j int) bool { return r.Largest[i].Tokens > r.Largest[j].Tokens })
	if len(r.Largest) > 5 {
		r.Largest = r.Largest[:5]
	}
	r.Total = a.EstimateTokens()
	return r
}

// Pin keeps a note in the system prompt for the rest of the session.
func (a *Agent) Pin(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	a.Pins = append(a.Pins, text)
}

// Unpin removes the n-th pin (1-based); it reports whether one existed.
func (a *Agent) Unpin(n int) bool {
	if n < 1 || n > len(a.Pins) {
		return false
	}
	a.Pins = append(a.Pins[:n-1], a.Pins[n:]...)
	return true
}
