package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// statsCommand prints cumulative counters for this process: what the
// model did, what it cost and which tools carried the work.
func (m *model) statsCommand() {
	ag := m.opts.Agent
	st := ag.Stats()
	u := ag.UsageSnapshot()
	var sb strings.Builder
	fmt.Fprintf(&sb, "session %s · open %s · %s · %s", m.opts.Session.Label(), textfmt.Duration(time.Since(m.openedAt)), textfmt.Plural(st.Turns, "turn"), textfmt.Plural(st.ModelCalls, "model call"))
	if st.ModelErrors > 0 {
		fmt.Fprintf(&sb, " (%d failed)", st.ModelErrors)
	}
	if st.ModelCalls > 0 {
		fmt.Fprintf(&sb, " · %s waiting on the model (avg %s)", textfmt.Duration(st.ModelTime), textfmt.Duration(st.ModelTime/time.Duration(st.ModelCalls)))
	}
	sb.WriteString("\ntokens · " + u.TokenLine())
	if pct, ok := ag.CacheRate(); ok {
		fmt.Fprintf(&sb, " · cache hit %d%%", pct)
	}
	if usd, ok := u.Cost(ag.Settings().Price); ok {
		sb.WriteString(" · cost " + llm.FormatUSD(usd))
	}
	if st.Saves() > 0 {
		var saves []string
		if st.Recoveries > 0 {
			saves = append(saves, textfmt.Plural(st.Recoveries, "tool call")+" recovered from text the model wrote instead of calling")
		}
		if st.WatchdogKills > 0 {
			saves = append(saves, textfmt.Plural(st.WatchdogKills, "stream")+" ended by the watchdog")
		}
		if st.Fallbacks > 0 {
			saves = append(saves, textfmt.Plural(st.Fallbacks, "call")+" that only worked on a fallback model")
		}
		if st.Retries > 0 {
			// A turn behind two 429s showed "↻ retried 2×" and then a
			// /stats reading "1 model call", with nothing to say the
			// call had been bought three times.
			saves = append(saves, textfmt.Plural(st.Retries, "call")+" that succeeded on a retry")
		}
		fmt.Fprintf(&sb, "\nrecovered · %s", strings.Join(saves, " · "))
	}
	fmt.Fprintf(&sb, "\ncontext ~%s of %s · %s · %s undoable", textfmt.Tokens(ag.EstimateTokens()), tokensOrUnknown(ag.CompactThreshold()), textfmt.Plural(st.Compactions, "compaction"), textfmt.Plural(len(ag.Checkpoints()), "turn"))
	if line := st.PromptLine(); line != "" {
		sb.WriteString("\n" + line)
	}
	if line := st.RouteLine(ag.RouteCheapName()); line != "" {
		sb.WriteString("\n" + line)
	}
	if m.opts.Jobs != nil {
		if jobs := m.opts.Jobs.List(); len(jobs) > 0 {
			running := 0
			for _, j := range jobs {
				if !j.Done {
					running++
				}
			}
			fmt.Fprintf(&sb, " · %s, %d running", textfmt.Plural(len(jobs), "background job"), running)
		}
	}
	calls, errs := st.ToolCalls()
	if calls == 0 {
		sb.WriteString("\nno tool calls yet")
	} else {
		fmt.Fprintf(&sb, "\n%s, %d failed:", textfmt.Plural(calls, "tool call"), errs)
		for _, t := range st.Tools {
			fmt.Fprintf(&sb, "\n  %-14s %10s", t.Name, textfmt.Plural(t.Calls, "call"))
			if t.Errors > 0 {
				fmt.Fprintf(&sb, "  %d failed", t.Errors)
			}
			fmt.Fprintf(&sb, "  %s total", textfmt.Duration(t.Time))
		}
	}
	// What you have never reached for, from counts kept on this machine
	// and nowhere else.
	if line := m.opts.Usage.Summary(m.opts.Features); line != "" {
		sb.WriteString("\n" + line)
	}
	m.transcript.info(sb.String())
}

func tokensOrUnknown(n int) string {
	if n <= 0 {
		return "no limit"
	}
	return textfmt.Tokens(n) + " compact threshold"
}
