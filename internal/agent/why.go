package agent

import (
	"fmt"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/session"
)

// The harness intervenes. It summarises a conversation that got long,
// stops a turn that was going in circles, sends the model back to fix a
// failing test, falls back to another model when a quota runs out, kills
// a stream that stopped producing, refuses a tool call, contradicts an
// answer that claimed more than the turn showed.
//
// Each of those leaves a record in the session ledger, and each is
// surprising the first time it happens. /why reads the ledger and says
// what the last one was in plain words. It makes no model call: the
// explanation is a lookup on the kind of intervention plus the text the
// harness already wrote, which is the whole point — asking the model why
// the harness did something is asking the wrong party, and paying for
// the answer.

// InterventionKind names what sort of thing the harness did.
type InterventionKind string

const (
	IvCompaction InterventionKind = "compaction"
	IvLoop       InterventionKind = "loop ladder"
	IvVerify     InterventionKind = "verify gate"
	IvVerdict    InterventionKind = "verdict"
	IvFallback   InterventionKind = "model fallback"
	IvWatchdog   InterventionKind = "watchdog"
	IvBudget     InterventionKind = "budget stop"
	IvRefused    InterventionKind = "refused tool"
	IvRecovery   InterventionKind = "tool-call recovery"
	IvArchive    InterventionKind = "archive fault"
	IvIntegrity  InterventionKind = "request integrity"
	IvHook       InterventionKind = "stop hook"
	IvPermission InterventionKind = "permission rule"
	IvSteering   InterventionKind = "steering"
	IvWorkingMem InterventionKind = "working memory"
	IvPathRule   InterventionKind = "path rule"
	IvPlan       InterventionKind = "plan check"
	IvNudge      InterventionKind = "nudge"
	IvOversized  InterventionKind = "oversized output"
	IvRetry      InterventionKind = "retried call"
)

// Intervention is one thing the harness did without being asked.
type Intervention struct {
	Seq  int
	At   time.Time
	Turn int
	Kind InterventionKind
	// Text is what the harness said at the time, verbatim.
	Text string
}

// Interventions reads a session ledger and returns what the harness did
// on its own, oldest first. Model output, user messages and ordinary
// tool calls are not interventions: they are the work.
func Interventions(events []session.Event) []Intervention {
	var out []Intervention
	for _, e := range events {
		iv := Intervention{Seq: e.Seq, At: e.Time, Turn: e.Turn}
		switch e.Type {
		case session.TypeCompactStart:
			iv.Kind, iv.Text = IvCompaction, textOr(e, "the conversation was summarised to free context")
		case session.TypeVerdict:
			// Only a verdict that disagrees. Every turn ends with one,
			// so counting the agreeable ones made /why answer "verdict
			// (turn 2) · nothing here edited code" after a session in
			// which the harness had done nothing at all — and buried
			// the compaction or the refusal that /why exists for behind
			// a row of them.
			if !e.IsError {
				continue
			}
			iv.Kind, iv.Text = IvVerdict, textOr(e, "")
			if iv.Text == "" {
				continue
			}
			if e.Name != "" {
				iv.Text = e.Name + ": " + iv.Text
			}
		case session.TypeNotice:
			iv.Kind, iv.Text = classifyNotice(textOr(e, "")), textOr(e, "")
			if iv.Text == "" {
				continue
			}
		case session.TypeToolResult:
			// A refusal is an intervention; every other tool error is
			// the model's own business.
			if !e.IsError || !refusalRe(e.Output) {
				continue
			}
			iv.Kind = IvRefused
			iv.Text = strings.TrimSpace(firstLineOf(e.Output))
			if e.Name != "" {
				iv.Text = e.Name + ": " + iv.Text
			}
		default:
			continue
		}
		out = append(out, iv)
	}
	return out
}

func textOr(e session.Event, fallback string) string {
	for _, s := range []string{e.Output, e.Reason} {
		if strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	if e.Message != nil && strings.TrimSpace(e.Message.Text()) != "" {
		return strings.TrimSpace(e.Message.Text())
	}
	return fallback
}

// refusalRe recognises a tool result that was refused rather than one
// that failed. Permission is the harness's decision; a compiler error is
// not.
func refusalRe(out string) bool {
	l := strings.ToLower(out)
	return strings.Contains(l, "denied by the user") ||
		strings.Contains(l, "permission denied") ||
		strings.Contains(l, "the user denied") ||
		strings.Contains(l, "refused: ") ||
		strings.Contains(l, "not permitted") ||
		strings.Contains(l, "read-only mode") ||
		// "Denied by permission rule read_file." — the text a --deny
		// rule actually produces. The pattern here used to read "denied
		// by a permission rule", an article that nothing writes, so the
		// one intervention a user is most likely to ask /why about was
		// the one kind it could not see.
		strings.Contains(l, "denied by permission rule") ||
		strings.Contains(l, "denied by a permission rule")
}

// classifyNotice works out what kind of intervention a harness note was
// from the note itself. The notes are written in one place each and
// their openings are stable, which is cheaper and clearer than carrying
// a kind through every emit site.
func classifyNotice(text string) InterventionKind {
	l := strings.ToLower(text)
	switch {
	case strings.HasPrefix(l, "retried "):
		return IvRetry
	case strings.HasPrefix(l, "turn stopped:"):
		return IvLoop
	case strings.HasPrefix(l, "verifying:"), strings.Contains(l, "sending the model back to fix"),
		strings.Contains(l, "giving up after"), strings.HasPrefix(l, "verification"):
		return IvVerify
	case strings.Contains(l, "falling back to"), strings.Contains(l, "cannot fall back"),
		strings.Contains(l, "this session continues on"):
		return IvFallback
	case strings.Contains(l, "stream stalled"), strings.Contains(l, "watchdog"),
		strings.Contains(l, "produced nothing for"):
		return IvWatchdog
	case strings.Contains(l, "spent"), strings.Contains(l, "max-cost"), strings.Contains(l, "budget"):
		return IvBudget
	case strings.Contains(l, "instead of calling it"):
		return IvRecovery
	case strings.HasPrefix(l, "archive fault"):
		return IvArchive
	case strings.HasPrefix(l, "request integrity:"):
		return IvIntegrity
	case strings.HasPrefix(l, "stop hook"):
		return IvHook
	case strings.Contains(l, "permission rule"):
		return IvPermission
	case strings.HasPrefix(l, "steering the turn:"):
		return IvSteering
	case strings.HasPrefix(l, "working memory:"):
		return IvWorkingMem
	case strings.Contains(l, "context pruned"), strings.Contains(l, "context summary"):
		return IvCompaction
	case strings.Contains(l, "claims more than this turn shows"):
		return IvVerdict
	case strings.Contains(l, "too large"), strings.Contains(l, "archived as "):
		return IvOversized
	case strings.Contains(l, "plan"):
		return IvPlan
	}
	return IvNudge
}

// because is the plain-words explanation of each kind: what the harness
// was doing and why, in the second person, without jargon.
var because = map[InterventionKind]string{
	IvCompaction: "the conversation had grown close to the context limit, so janetcode summarised the older part of it and kept the recent turns whole. The exact text of everything it removed is still in the archive, and the model reads it back with expand_output; /context shows what is in the window now.",
	IvLoop:       "the model was repeating itself — the same tool call, or the same cycle of calls, with nothing new coming back — so the turn was stopped rather than left to spend. Everything it had already changed is kept; telling it what to do differently is usually enough.",
	IvVerify:     "the project has verification commands (/verify), they were run after a turn that changed code, and the result was handed back to the model to act on. This is the gate doing its job, not an error.",
	IvVerdict:    "janetcode watched what the turn actually did — which files changed, which commands ran and what they exited with — and compared it with what the model said about it. The verdict is the harness's account, not the model's.",
	IvFallback:   "the model call failed in a way another model could survive (an exhausted quota, or rate limits and server errors that outlived the provider's own retries), so the call was retried on the next model in fallback_models. The partial answer from the failed attempt was discarded.",
	IvWatchdog:   "the streamed model call stopped producing for longer than the idle limit, so it was ended rather than left hanging. stream_idle_timeout and stream_max_duration in the config set those bounds.",
	IvBudget:     "the session reached the spending limit it was given — /budget in the terminal, --max-cost headless — so it stopped after the model call that crossed it. /usage has the breakdown, and /budget off removes the ceiling.",
	IvRefused:    "the tool call was not allowed: you refused it, a deny rule matched it, or the session is read-only. The model was told so and can work around it; /permissions shows the rules in force.",
	IvRecovery:   "the model wrote a tool call as text instead of calling it. janetcode read the text back as a call and ran it, so the turn carried on rather than stalling on a formatting mistake.",
	IvArchive:    "the model asked for a value it no longer had in context, and janetcode fetched the matching excerpts out of the archive and gave them back instead of letting it guess.",
	IvIntegrity:  "janetcode checked the request it was about to send and found something wrong with its shape — an unanswered tool call, a duplicated id — and repaired it before sending.",
	IvHook:       "a stop hook you have configured decided the turn was not finished and sent the model back to work. /hooks shows what is configured.",
	IvPermission: "a permission rule was saved (or could not be), so calls of that shape will not ask again. /permissions lists them.",
	IvSteering:   "you handed a note to the turn while it was running (ctrl+s), and it was delivered before the model's next call rather than starting a new turn.",
	IvWorkingMem: "the model was asked for a structured record to carry across the compaction and answered with prose, so it was kept as notes rather than discarded.",
	IvPathRule:   "a path-scoped project rule matched a file the turn touched, so its text was sent to the model once. /rules shows which rules exist and which have been sent.",
	IvPlan:       "janetcode checked the model's plan against what it was actually doing and said so.",
	IvOversized:  "a tool returned more than fits sensibly in the context, so it was archived and the model was given the head of it plus the id to read the rest with.",
	IvRetry:      "the model call failed in a way that usually passes on a second try — a rate limit, a 5xx, a connection dropped mid-stream — so janetcode waited and asked again on the same model, backing off a little longer each time. Nothing was lost and you were not asked; the turn carried on. A failure that another model could survive is a fallback instead, and /usage counts what the extra attempts cost.",
	IvNudge:      "janetcode put a note in front of the model about how the turn was going. Nudges are how the harness steers without taking over.",
}

// Why renders one intervention as a paragraph. It is a lookup and some
// string joining: no model call, and no I/O beyond reading the ledger
// the session already has open.
func (iv Intervention) Why() string {
	when := ""
	if !iv.At.IsZero() {
		when = " · " + iv.At.Format("15:04:05")
	}
	head := fmt.Sprintf("%s (turn %d%s)", iv.Kind, iv.Turn, when)
	reason := because[iv.Kind]
	if reason == "" {
		reason = "janetcode did this on its own; /trajectory #" + fmt.Sprint(iv.Seq) + " has the record."
	}
	return head + "\n" + iv.Text + "\n\n" + strings.ToUpper(reason[:1]) + reason[1:]
}

// WhyLast explains the nth most recent intervention, counting from 1.
// It returns a message rather than an error when there is nothing to
// explain, because "nothing has happened yet" is the answer.
func WhyLast(events []session.Event, n int) string {
	ivs := Interventions(events)
	if len(ivs) == 0 {
		return "the harness has not intervened in this session: nothing has been summarised, stopped, verified, refused or fallen back. /trajectory has the whole ledger."
	}
	if n < 1 {
		n = 1
	}
	if n > len(ivs) {
		return fmt.Sprintf("only %s in this session; /why %d is the oldest", plural(len(ivs), "harness intervention"), len(ivs))
	}
	iv := ivs[len(ivs)-n]
	out := iv.Why()
	if len(ivs) > 1 && n == 1 {
		out += fmt.Sprintf("\n\n(%d in this session; /why 2 for the one before)", len(ivs))
	}
	return out
}
