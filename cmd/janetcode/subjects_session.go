package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// messagesReport is the conversation as the model will receive it, which
// is not what the transcript shows: pruned results, injected notes and
// compaction summaries are all in here and none of them were typed.
func messagesReport(a *agent.Agent, arg string) string {
	if a == nil {
		return "no agent"
	}
	msgs := a.Messages
	if len(msgs) == 0 {
		return "the conversation is empty"
	}
	if strings.HasPrefix(arg, "#") {
		i, err := strconv.Atoi(strings.TrimPrefix(arg, "#"))
		if err != nil || i < 1 || i > len(msgs) {
			return fmt.Sprintf("no message #%s (there are %d)", strings.TrimPrefix(arg, "#"), len(msgs))
		}
		m := msgs[i-1]
		var b strings.Builder
		fmt.Fprintf(&b, "message %d of %d · %s\n\n", i, len(msgs), m.Role)
		for _, blk := range m.Blocks {
			switch blk.Type {
			case llm.BlockText, llm.BlockThinking:
				fmt.Fprintf(&b, "%s\n", textfmt.Clip(strings.TrimSpace(blk.Text), 6000))
			case llm.BlockToolUse:
				fmt.Fprintf(&b, "[call %s %s] %s\n", blk.Name, blk.ID, textfmt.Clip(strings.TrimSpace(string(blk.Input)), 1000))
			case llm.BlockToolResult:
				tag := "result"
				if blk.IsError {
					tag = "error"
				}
				fmt.Fprintf(&b, "[%s %s]\n%s\n", tag, blk.ToolUseID, textfmt.Clip(strings.TrimSpace(blk.Text), 4000))
			case llm.BlockImage:
				fmt.Fprintf(&b, "[image %s]\n", blk.MediaType)
			}
		}
		return b.String()
	}
	limit := 30
	if n, err := strconv.Atoi(arg); err == nil && n > 0 {
		limit = n
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d messages (inspect messages #<n> for one in full)\n", len(msgs))
	from := 0
	if len(msgs) > limit {
		from = len(msgs) - limit
	}
	for i := from; i < len(msgs); i++ {
		m := msgs[i]
		kinds := map[string]int{}
		for _, blk := range m.Blocks {
			kinds[string(blk.Type)]++
		}
		var parts []string
		for _, k := range []string{"text", "thinking", "tool_use", "tool_result", "image"} {
			if kinds[k] > 0 {
				parts = append(parts, fmt.Sprintf("%d %s", kinds[k], k))
			}
		}
		fmt.Fprintf(&b, "  %-4d %-9s %-28s %s\n", i+1, m.Role, strings.Join(parts, ", "), textfmt.Clip(strings.TrimSpace(oneLine(m.Text())), 60))
	}
	return b.String()
}

func promptReport(a *agent.Agent) string {
	if a == nil {
		return "no agent"
	}
	out := a.System
	if extra := a.SystemExtra; extra != nil {
		if e := strings.TrimSpace(extra()); e != "" {
			out += "\n\n" + e
		}
	}
	if pins := a.PinsSnapshot(); len(pins) > 0 {
		out += "\n\n# Pins\n"
		for _, p := range pins {
			out += "- " + p + "\n"
		}
	}
	return out
}

func contextReport(a *agent.Agent) string {
	if a == nil {
		return "no agent"
	}
	c := a.Context()
	var b strings.Builder
	fmt.Fprintf(&b, "estimated %s of %s before compaction (%d%%)\n\n", tokensOf(c.Total), tokensOf(c.CompactAt), pct(c.Total, c.CompactAt))
	rows := []struct {
		name string
		n    int
	}{
		{"system prompt", c.System}, {"project context", c.Extra}, {"pins", c.Pins},
		{fmt.Sprintf("tool schemas (%d tools)", c.ToolCount), c.Tools}, {"deferred tools", c.Deferred},
		{"your messages", c.UserText}, {"assistant text", c.AssistantText}, {"reasoning", c.Thinking},
		{"tool calls", c.ToolCalls}, {"tool results", c.ToolResults}, {"images", c.Images},
	}
	for _, row := range rows {
		if row.n > 0 {
			fmt.Fprintf(&b, "  %-28s %8s  %3d%%\n", row.name, tokensOf(row.n), pct(row.n, c.Total))
		}
	}
	fmt.Fprintf(&b, "\n%d messages over %d turns\n", c.Messages, c.Turns)
	if len(c.ByTool) > 0 {
		b.WriteString("\nby tool:\n")
		for _, t := range c.ByTool {
			fmt.Fprintf(&b, "  %-20s %3d calls  %8s\n", t.Name, t.Calls, tokensOf(t.Tokens))
		}
	}
	if len(c.Largest) > 0 {
		b.WriteString("\nlargest results still in context:\n")
		for _, it := range c.Largest {
			fmt.Fprintf(&b, "  %8s  %s %s (message %d)\n", tokensOf(it.Tokens), it.Tool, textfmt.Clip(strings.TrimSpace(it.Subject), 50), it.Index)
		}
	}
	return b.String()
}

func sessionReport(s *session.Session, a *agent.Agent) string {
	if s == nil {
		return "no session file (this run is not being recorded)"
	}
	var b strings.Builder
	m := s.Meta
	fmt.Fprintf(&b, "id       %s\n", m.ID)
	fmt.Fprintf(&b, "title    %s\n", orNone(m.Title))
	fmt.Fprintf(&b, "started  %s\n", m.Created.Local().Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&b, "provider %s/%s\n", m.Provider, m.Model)
	fmt.Fprintf(&b, "cwd      %s\n", m.Cwd)
	if m.ForkOf != "" {
		fmt.Fprintf(&b, "forked   from %s\n", m.ForkOf)
	}
	fmt.Fprintf(&b, "file     %s", s.Path())
	if st, err := os.Stat(s.Path()); err == nil {
		fmt.Fprintf(&b, " (%s)", textfmt.Bytes(st.Size()))
	}
	b.WriteString("\n")
	ev := s.Events()
	live := len(session.CurrentSeqs(ev))
	fmt.Fprintf(&b, "events   %d recorded, %d messages on the current surface\n", len(ev), live)
	if len(m.Pins) > 0 {
		fmt.Fprintf(&b, "pins     %d\n", len(m.Pins))
		for _, p := range m.Pins {
			fmt.Fprintf(&b, "  - %s\n", textfmt.Clip(strings.TrimSpace(p), 100))
		}
	}
	if a != nil {
		u := a.UsageSnapshot()
		fmt.Fprintf(&b, "usage    in %s · cache %s · out %s\n", tokensOf(u.InputTokens), tokensOf(u.CacheReadTokens), tokensOf(u.OutputTokens))
	}
	return b.String()
}

// eventsReport lists the log. Without an argument it shows the tail; a
// number shows that many, a type filters by it, and "#seq" shows one whole.
func eventsReport(s *session.Session, arg string) string {
	if s == nil {
		return "no session file"
	}
	ev := s.Events()
	if len(ev) == 0 {
		return "no events yet"
	}
	if strings.HasPrefix(arg, "#") {
		want, err := strconv.Atoi(strings.TrimPrefix(arg, "#"))
		if err == nil {
			for _, e := range ev {
				if e.Seq == want {
					return eventDetail(ev, e)
				}
			}
		}
		return fmt.Sprintf("no event #%s", strings.TrimPrefix(arg, "#"))
	}
	limit := 40
	filter := ""
	if n, err := strconv.Atoi(arg); err == nil && n > 0 {
		limit = n
	} else if arg != "" {
		filter = strings.ToLower(arg)
	}
	hidden := session.Shadowed(ev)
	var rows []string
	for _, e := range ev {
		if filter != "" && !strings.Contains(strings.ToLower(e.Type+" "+e.Name), filter) {
			continue
		}
		rows = append(rows, fmt.Sprintf("%-5d %d/%-3d %-20s %-12s %6s %s",
			e.Seq, e.Turn, e.Step, e.Type, textfmt.Clip(strings.TrimSpace(e.Name), 12), durMs(e.DurationMs), session.ClassifyWith(hidden, e)))
	}
	if len(rows) == 0 {
		return "no events match " + arg
	}
	head := fmt.Sprintf("%d of %d events (inspect events #<seq> for one in full)\n", min(limit, len(rows)), len(rows))
	head += fmt.Sprintf("%-5s %-5s %-20s %-12s %6s %s\n", "seq", "t/s", "type", "name", "dur", "surface")
	if len(rows) > limit {
		rows = rows[len(rows)-limit:]
	}
	return head + strings.Join(rows, "\n")
}

func eventDetail(ev []session.Event, e session.Event) string {
	var b strings.Builder
	fmt.Fprintf(&b, "seq %d · %s · turn %d step %d · %s\n", e.Seq, e.Type, e.Turn, e.Step, session.Classify(ev, e))
	fmt.Fprintf(&b, "at %s\n", e.Time.Local().Format("15:04:05.000"))
	if e.Name != "" {
		fmt.Fprintf(&b, "name %s\n", e.Name)
	}
	if e.CallID != "" {
		fmt.Fprintf(&b, "call %s\n", e.CallID)
	}
	if e.DurationMs > 0 {
		fmt.Fprintf(&b, "duration %dms\n", e.DurationMs)
	}
	if e.Usage != nil {
		fmt.Fprintf(&b, "usage in %d · cache %d · out %d\n", e.Usage.InputTokens, e.Usage.CacheReadTokens, e.Usage.OutputTokens)
	}
	if len(e.Replaces) > 0 {
		fmt.Fprintf(&b, "replaces %v\n", e.Replaces)
	}
	if len(e.Shadowed) > 0 {
		fmt.Fprintf(&b, "shadowed %d earlier events\n", len(e.Shadowed))
	}
	if e.Arguments != "" {
		fmt.Fprintf(&b, "\narguments:\n%s\n", textfmt.Clip(strings.TrimSpace(e.Arguments), 2000))
	}
	body := e.Output
	if body == "" && e.Message != nil {
		body = e.Message.Text()
	}
	if body != "" {
		fmt.Fprintf(&b, "\n%s\n", textfmt.Clip(strings.TrimSpace(body), 4000))
	}
	return b.String()
}

func archiveReport(ar *agent.Archive, arg string) string {
	if ar == nil {
		return "no archive in this session"
	}
	if arg != "" {
		e, body, err := ar.Get(arg)
		if err != nil {
			return err.Error()
		}
		return fmt.Sprintf("%s · %s %s · %s · %s\n\n%s", e.ID, e.Name, e.Subject, textfmt.Bytes(int64(e.Size)),
			e.Created.Local().Format("15:04:05"), textfmt.Clip(strings.TrimSpace(body), 4000))
	}
	entries := ar.List()
	if len(entries) == 0 {
		return "nothing archived yet"
	}
	var b strings.Builder
	total := 0
	for _, e := range entries {
		total += e.Size
	}
	fmt.Fprintf(&b, "%d entries, %s on disk (inspect archive <id> for one)\n", len(entries), textfmt.Bytes(int64(total)))
	for _, e := range entries {
		fmt.Fprintf(&b, "  %-10s %-14s %-40s %8s %s\n", e.ID, e.Name, textfmt.Clip(strings.TrimSpace(e.Subject), 40), textfmt.Bytes(int64(e.Size)), e.Created.Local().Format("15:04:05"))
	}
	return b.String()
}

// retrievalReport is the record of every archive lookup: what was asked
// and whether it returned anything. A recall that failed because the
// search found nothing is not the same as one that failed for any other
// reason, and this is where the difference shows.
func retrievalReport(s *session.Session) string {
	if s == nil {
		return "no session file"
	}
	var rows []string
	hits, misses := 0, 0
	for _, e := range s.Events() {
		if e.Type != session.TypeRetrieval {
			continue
		}
		mark := "found"
		if e.IsError {
			mark = "nothing"
			misses++
		} else {
			hits++
		}
		rows = append(rows, fmt.Sprintf("  %-5d %-6s %-7s %-52s %s", e.Seq, e.Name, mark, textfmt.Clip(strings.TrimSpace(e.Arguments), 52), tokensOf(e.Tokens/4)))
	}
	if len(rows) == 0 {
		return "no archive lookups yet"
	}
	head := fmt.Sprintf("%d lookups: %d found something, %d found nothing\n", hits+misses, hits, misses)
	head += fmt.Sprintf("  %-5s %-6s %-7s %-52s %s\n", "seq", "mode", "result", "query", "size")
	return head + strings.Join(rows, "\n")
}

func compactionReport(s *session.Session) string {
	if s == nil {
		return "no session file"
	}
	var rows []string
	var totalIn, totalOut int
	for _, e := range s.Events() {
		if e.Type != session.TypeCompactSummary {
			continue
		}
		in, out := 0, 0
		if e.Usage != nil {
			in, out = e.Usage.InputTokens+e.Usage.CacheReadTokens, e.Usage.OutputTokens
			totalIn, totalOut = totalIn+in, totalOut+out
		}
		cost := "no summary call (the record was carried forward)"
		if in > 0 || out > 0 {
			cost = fmt.Sprintf("summary cost %s in / %s out", tokensOf(in), tokensOf(out))
		}
		rows = append(rows, fmt.Sprintf("  seq %-5d turn %-3d was ~%s, replaced %d messages · %s",
			e.Seq, e.Turn, tokensOf(e.Tokens), len(e.Shadowed), cost))
	}
	if len(rows) == 0 {
		return "the history has not been compacted in this session"
	}
	head := fmt.Sprintf("%d compactions · summaries cost %s in / %s out in total\n", len(rows), tokensOf(totalIn), tokensOf(totalOut))
	return head + strings.Join(rows, "\n")
}

// verdictsReport is the record of what each turn demonstrated. It is the
// answer to "did it really do that": the claim and the evidence side by
// side, decided from what the harness watched rather than from the answer.
func verdictsReport(s *session.Session) string {
	if s == nil {
		return "no session file"
	}
	var rows []string
	counts := map[string]int{}
	for _, e := range s.Events() {
		if e.Type != session.TypeVerdict {
			continue
		}
		counts[e.Name]++
		mark := "  "
		if e.IsError {
			mark = "! "
		}
		row := fmt.Sprintf("%sturn %-3d %-17s %s", mark, e.Turn, e.Name, e.Output)
		var o struct {
			Claimed     string   `json:"claimed"`
			Checks      []string `json:"checks"`
			TestsEdited []string `json:"tests_edited"`
		}
		if len(e.Extra) > 0 && json.Unmarshal(e.Extra, &o) == nil {
			if len(o.Checks) > 0 {
				row += fmt.Sprintf("\n            ran: %s", textfmt.Clip(strings.TrimSpace(strings.Join(o.Checks, " · ")), 80))
			}
			if len(o.TestsEdited) > 0 {
				row += fmt.Sprintf("\n            tests changed in the same turn: %s", textfmt.Clip(strings.TrimSpace(strings.Join(o.TestsEdited, ", ")), 70))
			}
			if o.Claimed != "" {
				row += fmt.Sprintf("\n            the answer said: %q", o.Claimed)
			}
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return "no turn has changed code yet"
	}
	var head strings.Builder
	head.WriteString("what each turn demonstrated:\n")
	for _, k := range []string{string(agent.VerdictVerified), string(agent.VerdictSelfChecked), string(agent.VerdictNotDemonstrated), string(agent.VerdictUnverified), string(agent.VerdictCheckFailed), string(agent.VerdictVacuous)} {
		if counts[k] > 0 {
			fmt.Fprintf(&head, "  %-17s %d\n", k, counts[k])
		}
	}
	head.WriteString("\n")
	return head.String() + strings.Join(rows, "\n")
}
