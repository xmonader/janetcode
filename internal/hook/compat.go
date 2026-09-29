package hook

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Claude Code and Qwen Code share one hook format: events named
// PreToolUse, UserPromptSubmit and so on; each a list of groups with a
// "matcher" and the "hooks" it runs; the event on stdin under names such
// as tool_name and hook_event_name; and answers in hookSpecificOutput.
// janetcode reads that format beside its own, so a hook written for
// either works when pasted into the "hooks" object unchanged. Only
// command hooks exist here; an http, prompt or function hook is refused
// by name rather than skipped.

// compatEvents maps the other harnesses' event names to janetcode's.
var compatEvents = map[string]string{
	"PreToolUse":         PreTool,
	"PostToolUse":        PostTool,
	"PostToolUseFailure": PostToolFailure,
	"PreCompact":         PreCompact,
	"PostCompact":        PostCompact,
	"Stop":               Stop,
	"StopFailure":        StopFailure,
	"UserPromptSubmit":   UserPrompt,
	"SessionStart":       SessionStart,
	"SessionEnd":         SessionEnd,
	"PermissionRequest":  PermissionAsk,
	"Notification":       Notification,
	"SubagentStart":      SubagentStart,
	"SubagentStop":       SubagentStop,
}

// compatName is the other harnesses' name for a janetcode event, sent as
// hook_event_name.
func compatName(event string) string {
	for k, v := range compatEvents {
		if v == event {
			return k
		}
	}
	return event
}

// decodeHooks reads one event's list. Each element is a janetcode hook
// ({"command": ...}) or a group ({"matcher": ..., "hooks": [...]}), whose
// command hooks become janetcode hooks with the group's matcher.
func decodeHooks(raw json.RawMessage) ([]Hook, error) {
	var elems []json.RawMessage
	if err := json.Unmarshal(raw, &elems); err != nil {
		return nil, err
	}
	var out []Hook
	for i, e := range elems {
		var probe struct {
			Hooks json.RawMessage `json:"hooks"`
		}
		if err := json.Unmarshal(e, &probe); err != nil {
			return nil, fmt.Errorf("[%d]: %w", i, err)
		}
		if probe.Hooks == nil {
			var h Hook
			if err := json.Unmarshal(e, &h); err != nil {
				return nil, fmt.Errorf("[%d]: %w", i, err)
			}
			out = append(out, h)
			continue
		}
		var g struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
				Timeout int    `json:"timeout"`
				Network *bool  `json:"network"`
			} `json:"hooks"`
		}
		if err := json.Unmarshal(e, &g); err != nil {
			return nil, fmt.Errorf("[%d]: %w", i, err)
		}
		for j, h := range g.Hooks {
			if h.Type != "" && h.Type != "command" {
				return nil, fmt.Errorf("[%d].hooks[%d]: %q hooks are not supported; only command hooks are", i, j, h.Type)
			}
			out = append(out, Hook{Match: g.Matcher, Command: h.Command, Timeout: h.Timeout, Network: h.Network})
		}
	}
	return out, nil
}

// toolAliases are the names other harnesses give janetcode's tools, so a
// matcher such as "Bash" or "Write|Edit" copied from their configs picks
// the same tools here. An alias is compared whole, never as a pattern.
var toolAliases = map[string][]string{
	"bash":          {"Bash", "run_shell_command"},
	"read_file":     {"Read"},
	"write_file":    {"Write"},
	"edit_file":     {"Edit", "MultiEdit", "edit"},
	"notebook_edit": {"NotebookEdit"},
	"fetch_url":     {"WebFetch", "web_fetch"},
	"web_search":    {"WebSearch"},
	"task":          {"Task", "Agent"},
	"todo":          {"TodoWrite", "todo_write"},
}

// matchesAlias reports whether a tool event's pattern names one of the
// tool's aliases.
func matchesAlias(pat, event, tool string) bool {
	switch event {
	case PreTool, PostTool, PostToolFailure, PermissionAsk:
	default:
		return false
	}
	aliases := toolAliases[tool]
	if len(aliases) == 0 {
		return false
	}
	re, err := matchRe("^(?:" + pat + ")$")
	if err != nil {
		return false
	}
	for _, a := range aliases {
		if re.MatchString(a) {
			return true
		}
	}
	return false
}

// MarshalJSON writes the event under janetcode's names and, beside them,
// the names Claude Code and Qwen Code hooks read, so a script written for
// either finds its fields.
func (in Input) MarshalJSON() ([]byte, error) {
	type plain Input
	base, err := json.Marshal(plain(in))
	if err != nil {
		return nil, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(base, &m); err != nil {
		return nil, err
	}
	put := func(k string, v any) {
		if _, taken := m[k]; taken {
			return
		}
		b, err := json.Marshal(v)
		if err == nil {
			m[k] = b
		}
	}
	put("hook_event_name", compatName(in.Event))
	if in.Tool != "" {
		put("tool_name", in.Tool)
	}
	if len(in.Input) > 0 {
		m["tool_input"] = in.Input
	}
	if in.Event == PostTool || in.Event == PostToolFailure {
		put("tool_response", in.Output)
	}
	if in.Text != "" {
		put("last_assistant_message", in.Text)
	}
	if in.Summary != "" {
		put("compact_summary", in.Summary)
	}
	if in.Agent != "" {
		put("agent_type", in.Agent)
	}
	if in.ErrorType != "" && in.Event == StopFailure {
		// Their stop_failure names the type "error" and the message
		// "error_details".
		m["error"], _ = json.Marshal(in.ErrorType)
		put("error_details", in.Error)
	}
	return json.Marshal(m)
}

// parseOutput reads a hook's JSON answer: janetcode's fields, and the
// ones Claude Code and Qwen Code hooks print -- "continue": false with a
// stopReason, systemMessage, and hookSpecificOutput's permissionDecision,
// updatedInput, additionalContext and the decision object of a
// permission request. ok is false when the text is not a JSON object.
func parseOutput(text string) (o Output, ok bool) {
	if !strings.HasPrefix(text, "{") {
		return Output{}, false
	}
	var raw struct {
		Decision      string          `json:"decision"`
		Reason        string          `json:"reason"`
		Input         json.RawMessage `json:"input"`
		Note          string          `json:"note"`
		Continue      json.RawMessage `json:"continue"`
		Message       string          `json:"message"`
		StopReason    string          `json:"stopReason"`
		SystemMessage string          `json:"systemMessage"`
		Specific      *struct {
			PermissionDecision       string          `json:"permissionDecision"`
			PermissionDecisionReason string          `json:"permissionDecisionReason"`
			UpdatedInput             json.RawMessage `json:"updatedInput"`
			AdditionalContext        string          `json:"additionalContext"`
			Decision                 *struct {
				Behavior     string          `json:"behavior"`
				Message      string          `json:"message"`
				UpdatedInput json.RawMessage `json:"updatedInput"`
			} `json:"decision"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(text), &raw); err != nil {
		return Output{}, false
	}
	o = Output{Decision: raw.Decision, Reason: raw.Reason, Input: raw.Input, Note: raw.Note, Message: raw.Message}
	if raw.SystemMessage != "" && o.Message == "" {
		o.Message = raw.SystemMessage
	}
	switch c := strings.TrimSpace(string(raw.Continue)); {
	case c == "false":
		// Theirs: stop here. Read as a block, with stopReason as why.
		o.Decision = "block"
		if o.Reason == "" {
			o.Reason = raw.StopReason
		}
	case strings.HasPrefix(c, `"`):
		_ = json.Unmarshal(raw.Continue, &o.Continue)
	}
	if sp := raw.Specific; sp != nil {
		if sp.PermissionDecision != "" {
			o.Decision = sp.PermissionDecision
			if sp.PermissionDecisionReason != "" {
				o.Reason = sp.PermissionDecisionReason
			}
		}
		if len(sp.UpdatedInput) > 0 {
			o.Input = sp.UpdatedInput
		}
		if sp.AdditionalContext != "" {
			o.Note = strings.TrimSpace(o.Note + "\n" + sp.AdditionalContext)
		}
		if d := sp.Decision; d != nil {
			o.Decision = d.Behavior
			if d.Message != "" {
				o.Reason = d.Message
			}
			if len(d.UpdatedInput) > 0 {
				o.Input = d.UpdatedInput
			}
		}
	}
	return o, true
}
