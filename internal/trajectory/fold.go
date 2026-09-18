package trajectory

import (
	"strconv"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/session"
)

// previewSource is how much of a body is read to make its one-line
// preview. A tool result can be a megabyte of test output; the first
// couple of kilobytes decide the line, and reading the rest to throw it
// away is what makes a ledger of a long session slow to draw.
const previewSource = 2048

// previewChars is how long a preview may be before the renderer clips it
// to the terminal. It is generous: clipping to the real width is the
// view's job, and a preview cut short here could not be widened again.
const previewChars = 512

// Fold turns a session's events into the ledger. Events the reader has
// no use for (step boundaries, bookkeeping) become structure rather than
// lines: a step/start opens a group, it is not a record of its own.
//
// The pass is single and forward. A tool result is joined to its call by
// CallID, which means a call whose result never arrived stays in the
// ledger as a running record instead of vanishing — a cancelled turn
// should look cancelled, not shorter.
func Fold(events []session.Event) Ledger {
	var l Ledger
	byCall := map[string]int{} // CallID → index into l.All
	stepStart := map[string]time.Time{}
	var cumulative Usage
	add := func(r Record) *Record {
		r.Index = len(l.All) + 1
		cumulative.add(r.Usage)
		r.Cumulative = cumulative
		l.All = append(l.All, r)
		return &l.All[len(l.All)-1]
	}
	stepKey := func(turn, step int) string { return strconv.Itoa(turn) + "/" + strconv.Itoa(step) }

	for _, e := range events {
		switch e.Type {
		case session.TypeStepStart:
			stepStart[stepKey(e.Turn, e.Step)] = e.Time

		case session.TypeUserMessage:
			body := messageText(e)
			add(Record{Seq: e.Seq, Turn: e.Turn, Step: e.Step, Kind: KindUser,
				Preview: preview(body), Detail: body, Started: e.Time})

		case session.TypeNotice:
			// A notice is the harness speaking into the conversation on
			// its own: a compaction warning, a rule that joined, a job
			// that finished. It is context the model was given.
			body := firstNonEmpty(e.Output, e.Reason, messageText(e))
			if strings.TrimSpace(body) == "" {
				continue
			}
			add(Record{Seq: e.Seq, Turn: e.Turn, Step: e.Step, Kind: KindContext,
				Preview: preview(body), Detail: body, Started: e.Time})

		case session.TypePlanMode:
			add(Record{Seq: e.Seq, Turn: e.Turn, Step: e.Step, Kind: KindSystem,
				Text: "plan mode", Preview: preview(e.Reason), Detail: e.Reason, Started: e.Time})

		case session.TypeAssistant:
			body := messageText(e)
			r := Record{Seq: e.Seq, Turn: e.Turn, Step: e.Step, Kind: KindMessage,
				Preview: preview(body), Output: body, Model: e.Model,
				Usage:  usageOf(e.Usage),
				TTFT:   time.Duration(e.TTFTMs) * time.Millisecond,
				Decode: time.Duration(e.DecodeMs) * time.Millisecond,
			}
			r.Started = stepStart[stepKey(e.Turn, e.Step)]
			switch {
			case e.DurationMs > 0:
				r.Duration = time.Duration(e.DurationMs) * time.Millisecond
			case r.TTFT+r.Decode > 0:
				r.Duration = r.TTFT + r.Decode
			case !r.Started.IsZero() && !e.Time.IsZero():
				r.Duration = e.Time.Sub(r.Started)
			}
			if r.Started.IsZero() && !e.Time.IsZero() {
				r.Started = e.Time.Add(-r.Duration)
			}
			if strings.TrimSpace(body) == "" {
				// A step that only dispatched tools still happened, and a
				// blank line would hide the model call that paid for it.
				r.Text = "(tool calls only)"
			}
			add(r)

		case session.TypeToolCall:
			r := Record{Seq: e.Seq, Turn: e.Turn, Step: e.Step, Kind: KindTool,
				Text: e.Name, Tool: e.Name, CallID: e.CallID, Intent: e.Intent,
				Preview: preview(e.Arguments), Detail: e.Arguments,
				Started: e.Time, Running: true}
			rec := add(r)
			if e.CallID != "" {
				byCall[e.CallID] = rec.Index - 1
			}

		case session.TypeToolResult:
			i, ok := byCall[e.CallID]
			if !ok {
				// A result with no call in the log: keep it rather than
				// drop it, because a result nobody asked for is exactly
				// the kind of thing a ledger is read to find.
				add(Record{Seq: e.Seq, Turn: e.Turn, Step: e.Step, Kind: KindTool,
					Text: e.Name, Tool: e.Name, CallID: e.CallID,
					Result: preview(e.Output), Output: e.Output,
					IsError: e.IsError, Denied: e.Denied, Started: e.Time})
				continue
			}
			r := &l.All[i]
			r.Running = false
			r.Result = preview(e.Output)
			r.Output = e.Output
			r.IsError = e.IsError
			r.Denied = e.Denied
			switch {
			case e.DurationMs > 0:
				r.Duration = time.Duration(e.DurationMs) * time.Millisecond
			case !r.Started.IsZero() && !e.Time.IsZero():
				r.Duration = e.Time.Sub(r.Started)
			}

		case session.TypeCompactStart:
			add(Record{Seq: e.Seq, Turn: e.Turn, Step: e.Step, Kind: KindCompacted,
				Text: "compacting the context", Started: e.Time, Running: true})

		case session.TypeCompactSummary, session.TypeCompactEnd:
			r := lastOfKind(l.All, KindCompacted)
			body := firstNonEmpty(e.Output, messageText(e))
			if r == nil {
				add(Record{Seq: e.Seq, Turn: e.Turn, Step: e.Step, Kind: KindCompacted,
					Text: "context compacted", Preview: preview(body), Output: body,
					Usage: usageOf(e.Usage), Started: e.Time})
				continue
			}
			r.Running = false
			r.Text = "context compacted"
			if body != "" {
				r.Preview, r.Output = preview(body), body
			}
			r.Usage.add(usageOf(e.Usage))
			if !r.Started.IsZero() && !e.Time.IsZero() && r.Duration == 0 {
				r.Duration = e.Time.Sub(r.Started)
			}

		case session.TypeVerdict:
			body := firstNonEmpty(e.Reason, e.Output)
			if strings.TrimSpace(body) == "" {
				continue
			}
			add(Record{Seq: e.Seq, Turn: e.Turn, Step: e.Step, Kind: KindVerdict,
				Preview: preview(body), Detail: body, Started: e.Time})
		}
	}
	l.Turns = group(l.All)
	return l
}

// group builds the turn/step tree over the records. Records before a
// turn's first step belong to its "Message" group: what you asked and
// what the harness attached before anything was called.
func group(all []Record) []Turn {
	var turns []Turn
	at := map[int]int{} // turn number → index into turns
	for _, r := range all {
		ti, ok := at[r.Turn]
		if !ok {
			turns = append(turns, Turn{Turn: r.Turn})
			ti = len(turns) - 1
			at[r.Turn] = ti
		}
		t := &turns[ti]
		title, step := "Message", 0
		if r.Step > 0 {
			title, step = "Step "+strconv.Itoa(r.Step), r.Step
		}
		gi := -1
		for i := range t.Groups {
			if t.Groups[i].Title == title {
				gi = i
				break
			}
		}
		if gi < 0 {
			t.Groups = append(t.Groups, Group{Title: title, Step: step})
			gi = len(t.Groups) - 1
		}
		t.Groups[gi].Records = append(t.Groups[gi].Records, r)
	}
	return turns
}

func lastOfKind(all []Record, k Kind) *Record {
	for i := len(all) - 1; i >= 0; i-- {
		if all[i].Kind == k && all[i].Running {
			return &all[i]
		}
	}
	return nil
}

func messageText(e session.Event) string {
	if e.Message == nil {
		return ""
	}
	return e.Message.Text()
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// preview squeezes a body onto one line: at most previewSource bytes are
// read, every run of whitespace becomes a single space, and the result is
// cut to previewChars with an ellipsis. Reading past previewSource would
// only be thrown away.
func preview(s string) string {
	if len(s) > previewSource {
		s = s[:previewSource]
	}
	var b strings.Builder
	b.Grow(len(s))
	space := false
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			space = true
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(r)
	}
	out := b.String()
	if len([]rune(out)) > previewChars {
		out = string([]rune(out)[:previewChars]) + "…"
	}
	return out
}
