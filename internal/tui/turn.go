package tui

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/hints"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/prompt"
	"github.com/xmonader/janetcode/internal/textfmt"
)

func (m *model) submit(text string) tea.Cmd { return m.submitAs(text, "") }

// submitAs sends text to the model but shows note in its place when the
// prompt is machinery rather than something the user wrote. A goal round
// used to appear in the transcript as a user message carrying the whole
// continuation, housekeeping paragraph included, as though the user had
// typed "(This is round 1 of at most 8. Rounds are a budget, not a
// plan...)".
func (m *model) submitAs(text, note string) tea.Cmd {
	text = m.inflatePastes(text)
	if m.opts.Agent.Provider == nil {
		m.transcript.errorf("not connected to a provider yet: run /connect")
		m.refresh(false)
		return nil
	}
	if note != "" {
		m.transcript.info(note)
	} else {
		m.transcript.user(text)
	}
	// A host the user just named is a host the user is asking about, so
	// fetch_url may reach it even on a private network. Only main seeded
	// this, from the first prompt, which left "look at
	// http://localhost:8080/health" refused in the message that asked for
	// it.
	m.opts.Typed.Note(text)
	m.lastUserText = text
	text, atts := prompt.ExpandMentions(m.opts.Cwd, text)
	if len(atts) > 0 {
		m.noteUse("@path")
		names := make([]string, len(atts))
		for i, a := range atts {
			names[i] = a.Path
			switch {
			case a.Image():
				names[i] += " (image, " + textfmt.Bytes(a.Size) + ")"
			case !a.Dir:
				names[i] += " (" + textfmt.Bytes(a.Size) + ")"
			}
		}
		m.transcript.info(noteAttached + "attached " + strings.Join(names, " · "))
	}
	blocks := append([]llm.Block{{Type: llm.BlockText, Text: text}}, prompt.ImageBlocks(atts)...)
	m.startTurn("thinking")
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	ag, send, queue := m.opts.Agent, m.send, m.queue
	m.repin()
	m.refresh(false)
	go func() {
		// Every message reached the session file as it happened; there is
		// nothing left to write when the turn ends.
		err := ag.RunBlocks(ctx, blocks, queue.push)
		send(agentDoneMsg{err: err})
	}()
	return m.spinner.Tick
}

func (m *model) startTurn(phase string) {
	m.steering = nil
	m.term.turnStarted()
	m.running = true
	m.cancelling = false
	m.startedAt = time.Now()
	m.status = ""
	m.lastVerdict = ""
	m.setPhase(phase)
	if phase == "thinking" {
		m.transcript.apply(agent.Event{Kind: agent.EvThinking})
	}
}

// onOff names a toggle's new state, so every "/x is on" line reads the
// same way.
func onOff(v bool) string {
	if v {
		return "on"
	}
	return "off"
}

// compact summarises the history in the background like a turn.
func (m *model) compact() tea.Cmd {
	m.startTurn("compacting context")
	m.compacting = true
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	ag, send, queue := m.opts.Agent, m.send, m.queue
	m.note(fmt.Sprintf("compacting ~%s tokens of context…", textfmt.Tokens(ag.EstimateTokens())))
	m.refresh(false)
	go func() {
		// Compaction appends the summary and the seqs it replaces to the
		// same log; the session file stays as it is.
		err := ag.ForceCompact(ctx, queue.push)
		if errors.Is(err, agent.ErrNothingToCompact) {
			err = nil
		}
		send(agentDoneMsg{err: err})
	}()
	return m.spinner.Tick
}

func (m *model) cancelTurn() {
	if m.cancel != nil {
		m.cancel()
	}
	m.cancelling = true
	m.setPhase("cancelling")
	m.status = "cancelling… (ctrl+c again to force quit)"
}

func (m *model) finishTurn(msg agentDoneMsg) tea.Cmd {
	m.running = false
	m.cancelling = false
	compacting := m.compacting
	m.compacting = false
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	m.pending = nil
	m.pendingClosedAt = time.Now()
	m.setPhase("")
	elapsed := textfmt.Duration(time.Since(m.startedAt))
	m.transcript.finishTurn()
	switch {
	case errors.Is(msg.err, context.Canceled):
		m.transcript.dropEmptyTrailing()
		m.transcript.abortUnfinished("cancelled")
		m.transcript.errorf("cancelled after %s", elapsed)
		m.status = "cancelled"
	case errors.Is(msg.err, agent.ErrMaxIterations):
		m.transcript.errorf("stopped after %d tool rounds in one turn · send a message to continue", m.opts.Agent.Settings().MaxIterations)
		m.status = "stopped"
	case errors.Is(msg.err, agent.ErrStuck):
		m.transcript.errorf("stopped: the model kept repeating the same tool call · its changes so far are kept; tell it what to do differently")
		m.status = "stuck"
	case msg.err != nil:
		m.transcript.dropEmptyTrailing()
		m.transcript.abortUnfinished("the turn ended before this call returned")
		m.transcript.errorf("%s", friendlyError(msg.err, m.opts.Provider))
		m.status = "error"
	case compacting:
		m.status = "compacted in " + elapsed
	default:
		m.status = "done in " + elapsed
		if b := verdictBrief(m.lastVerdict); b != "" {
			m.status = b + " · " + m.status
		}
		if m.lastTurn != "" {
			m.status += " · " + m.lastTurn
		}
		if n := m.filesChangedLastTurn(); n > 0 {
			m.status += fmt.Sprintf(" · %s changed · /undo reverts", textfmt.Plural(n, "file"))
		}
	}
	// The end of a turn is where the hint rules get their chance: the
	// counters are settled, nothing is being asked of the user, and a
	// line here is read rather than scrolled past.
	m.noteHint(func(s *hints.State) {
		if m.filesChangedLastTurn() > 0 {
			s.MutatingTurns++
		}
	})
	m.reportCacheWaste()
	m.steering = nil
	m.term.turnEnded(msg.err == nil, m.status)
	if msg.err == nil && len(m.queued) == 0 {
		if cmd := m.goalRound(); cmd != nil {
			return cmd
		}
	}
	if len(m.queued) == 0 {
		return nil
	}
	if msg.err != nil {
		// Do not fire queued prompts into a failed or cancelled turn; hand
		// them back so nothing typed is lost.
		m.restoreQueued()
		return nil
	}
	next := m.queued[0]
	m.queued = m.queued[1:]
	return m.submit(next)
}

// friendlyError turns a provider failure into one shape:
//
//	Headline (code): why. What to do.
//
// The headline says what failed in three words, the code is what you
// would search for, the reason is the provider's own words, and the last
// sentence is the one thing to try. A bare Go error is none of those.
func friendlyError(err error, provider string) string {
	s := strings.TrimSpace(err.Error())
	l := strings.ToLower(s)
	shape := func(head, code, todo string) string {
		if code != "" {
			head += " (" + code + ")"
		}
		why := strings.TrimRight(s, ".")
		return head + ": " + why + ". " + todo
	}
	switch {
	case strings.Contains(l, "connection refused") || strings.Contains(l, "no such host") || strings.Contains(l, "dial tcp"):
		return shape("cannot reach the API", errorCode(l), "Check the base URL for provider "+provider+" with /connect, and your network.")
	case strings.Contains(l, "http 401") || strings.Contains(l, "authentication") || strings.Contains(l, "invalid x-api-key") || strings.Contains(l, "incorrect api key"):
		return shape("authentication failed", errorCode(l), "Check the API key for provider "+provider+" with /connect.")
	case strings.Contains(l, "http 429") || strings.Contains(l, "rate limit") || strings.Contains(l, "quota"):
		return shape("rate limited or out of quota", errorCode(l), "Wait and resend, add keys with /connect, or switch model with /model.")
	case strings.Contains(l, "http 404") || strings.Contains(l, "model not found") || strings.Contains(l, "not_found"),
		strings.Contains(l, "http 400") && strings.Contains(l, "model"):
		return shape("model not available", errorCode(l), "Pick another with /model.")
	case strings.Contains(l, "stream stalled"):
		return shape("the model stopped sending", errorCode(l), "Resend it; raise stream_idle_timeout or stream_max_duration in the config if it keeps happening on long answers.")
	case strings.Contains(l, "deadline exceeded") || strings.Contains(l, "timeout"):
		return shape("the request timed out", errorCode(l), "Resend it; if it keeps happening the model may be overloaded.")
	case strings.Contains(l, "http 5"):
		return shape("the provider failed", errorCode(l), "Resend it; the failure is on their side.")
	}
	return s
}

// errorCode is the short thing you would search for: the HTTP status when
// there is one, otherwise the kind of network failure.
var httpStatusRe = regexp.MustCompile(`(?:http |status |code )(\d{3})`)

func errorCode(l string) string {
	if m := httpStatusRe.FindStringSubmatch(l); m != nil {
		return "http " + m[1]
	}
	switch {
	case strings.Contains(l, "no such host"):
		return "dns"
	case strings.Contains(l, "connection refused"):
		return "refused"
	case strings.Contains(l, "stream stalled"):
		return "stalled stream"
	case strings.Contains(l, "deadline exceeded") || strings.Contains(l, "timeout"):
		return "timeout"
	}
	return ""
}

// restoreQueued puts queued messages back in the input, under whatever is
// already there, separated by blank lines. Nothing typed is ever thrown
// away by a cancel: it goes back where it came from.
func (m *model) restoreQueued() {
	if len(m.queued) == 0 {
		return
	}
	parts := m.queued
	m.queued = nil
	if v := strings.TrimSpace(m.input.Value()); v != "" {
		parts = append(parts, v)
	}
	m.input.SetValue(strings.Join(parts, "\n\n"))
	m.input.CursorEnd()
	m.layout()
	m.refresh(false)
}

// reportCacheWaste says when a turn paid a second time for a prefix the
// turn before had cached. It is invisible otherwise: the turn works, the
// answer is right, and the bill is several times larger.
func (m *model) reportCacheWaste() {
	n, cause, idle := m.opts.Agent.TurnCacheWaste()
	if n == 0 {
		return
	}
	head := "cache miss"
	if idle > time.Minute {
		head += " after " + textfmt.Duration(idle.Round(time.Minute)) + " idle"
	}
	line := fmt.Sprintf("%s: %s tokens re-billed", head, textfmt.Tokens(n))
	if usd, ok := (llm.Usage{InputTokens: n}).Cost(m.opts.Agent.Price); ok {
		line += " (~" + llm.FormatUSD(usd) + ")"
	}
	m.note(line + " · " + cause)
}
