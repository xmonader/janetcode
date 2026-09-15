package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/perm"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// maxPinFile caps how much of a /pin @path file lands in the system prompt.
const maxPinFile = 16 * 1024

// systemPrompt is the full system text sent with a request: the base
// prompt, the dynamic extra (memory and skill indexes), pins and modes.
func (a *Agent) systemPrompt() string {
	system := a.SystemPrompt() + a.extra()
	if pins := a.PinsSnapshot(); len(pins) > 0 {
		var sb strings.Builder
		sb.WriteString("\n\n# Pinned by the user\nThese notes stay in context for the whole session, including after compaction.\n")
		for i, p := range pins {
			fmt.Fprintf(&sb, "\n%d. %s\n", i+1, p)
		}
		system += sb.String()
	}
	return system + a.modeSystem()
}

// CacheRate is the share of session input tokens served from the
// provider's prompt cache, and whether the provider reported any.
func (a *Agent) CacheRate() (pct int, ok bool) {
	u := a.UsageSnapshot()
	total := u.InputTokens + u.CacheReadTokens + u.CacheWriteTokens
	if total == 0 || u.CacheReadTokens+u.CacheWriteTokens == 0 {
		return 0, false
	}
	return u.CacheReadTokens * 100 / total, true
}

// ContextReport breaks the context window down by what fills it.
type ContextReport struct {
	Total, CompactAt                                          int
	System, Extra, Pins, Tools, ToolCount, Deferred           int
	UserText, AssistantText, Thinking, ToolCalls, ToolResults int
	Images, Messages, Turns                                   int
	ByTool                                                    []ToolShare
	Largest                                                   []ContextItem
	// Prefix contributors inside System and Extra. System is Base+Project;
	// Extra is Memory+Skills (unlabelled extra is counted in Extra only).
	// Modes is the read-only / plan / accept text appended after System;
	// it is sent but was not in System, so it is listed separately.
	Base, Project, Modes, Memory, Skills, PinCount int
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
	r := ContextReport{CompactAt: a.Settings().CompactAt}
	if r.CompactAt == 0 {
		r.CompactAt = DefaultCompactAt
	}
	sys := a.SystemPrompt()
	r.System = len(sys) / 4
	r.Base, r.Project = splitPromptTokens(sys)
	r.Modes = len(a.modeSystem()) / 4
	extra := a.extra()
	r.Extra = len(extra) / 4
	r.Memory, r.Skills = splitExtraTokens(extra)
	pins := a.PinsSnapshot()
	r.PinCount = len(pins)
	for _, p := range pins {
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

// PrefixLines is the system-prompt breakdown /context and --inspect
// context share: where the prefix goes, in tokens.
func (r ContextReport) PrefixLines() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "  base          %6s   project %6s   modes %6s\n", textfmt.Tokens(r.Base), textfmt.Tokens(r.Project), textfmt.Tokens(r.Modes))
	fmt.Fprintf(&sb, "  memory        %6s   skills  %6s   pins  %6s (%d)\n", textfmt.Tokens(r.Memory), textfmt.Tokens(r.Skills), textfmt.Tokens(r.Pins), r.PinCount)
	return sb.String()
}

// splitPromptTokens cuts SystemPrompt into the harness's own text and
// the project's instruction files (see prompt.writeEnvironment).
func splitPromptTokens(s string) (base, project int) {
	const mark = "\n# Project instructions"
	i := strings.Index(s, mark)
	if i < 0 {
		return len(s) / 4, 0
	}
	return i / 4, (len(s) - i) / 4
}

// splitExtraTokens cuts the memory+skill index SystemExtra concatenates.
func splitExtraTokens(s string) (memory, skills int) {
	const skillsMark = "\n# Skills\n"
	if i := strings.Index(s, skillsMark); i >= 0 {
		return i / 4, (len(s) - i) / 4
	}
	if strings.Contains(s, "\n# Memory\n") || strings.HasPrefix(s, "# Memory\n") {
		return len(s) / 4, 0
	}
	return 0, 0
}

// PinText resolves what /pin should store: arg unchanged, or for an
// unquoted @path argument the file's content as of now, capped and
// prefixed so the pin reads as a snapshot rather than a live reference.
func PinText(cwd, arg string) (string, error) {
	if !strings.HasPrefix(arg, "@") || strings.ContainsAny(arg, " \t") {
		return arg, nil
	}
	rel := strings.TrimPrefix(arg, "@")
	data, err := os.ReadFile(filepath.Join(cwd, rel))
	if err != nil {
		return "", err
	}
	body := string(data)
	if len(body) > maxPinFile {
		body = body[:maxPinFile] + "\n… (truncated)"
	}
	return "File " + rel + " as of pinning:\n" + body, nil
}
