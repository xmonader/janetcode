package trajectory

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/xmonader/janetcode/internal/textfmt"
)

// The ledger line, in columns:
//
//	#12  TOOL     bash · go test ./...  → ok all 2.0s            2.0s
//	#13  MODEL    The suite passes and the tree is formatted.  1.2k↑ 30↓ 1.0s
//
// The left gutter is fixed so the tags line up and the eye can run down
// them; the content takes whatever is left; the metrics are right-
// aligned so they form their own column without a separator.
const (
	indexWidth = 5 // "#123"
	tagWidth   = 8 // the longest tag is COMPACT
	metricGap  = 2
	minContent = 20
)

// Options are what the caller knows and the ledger does not: how wide
// the terminal is, and whether the reader asked for everything.
type Options struct {
	Width int
	// Turn limits the ledger to one turn. Zero shows all of them.
	Turn int
	// Last limits the ledger to the most recent n records, which is what
	// makes /trajectory usable in a session that has run for an hour.
	// Zero shows everything.
	Last int
}

const defaultWidth = 100

func (o Options) width() int {
	if o.Width <= 0 {
		return defaultWidth
	}
	return o.Width
}

// Format renders the ledger: a header per turn, a header per step with
// its span and the tools it ran, and one line per record.
func Format(l Ledger, o Options) string {
	if len(l.All) == 0 {
		return "no trajectory yet: no turn has run in this session"
	}
	turns := l.Turns
	if o.Turn > 0 {
		turns = nil
		for _, t := range l.Turns {
			if t.Turn == o.Turn {
				turns = append(turns, t)
			}
		}
		if len(turns) == 0 {
			return "no turn " + strconv.Itoa(o.Turn) + " in this session"
		}
	}
	keep := map[int]bool{}
	elided := 0
	if o.Last > 0 && len(l.All) > o.Last {
		for _, r := range l.All[len(l.All)-o.Last:] {
			keep[r.Index] = true
		}
		elided = len(l.All) - o.Last
	}
	var sb strings.Builder
	if elided > 0 {
		fmt.Fprintf(&sb, "%s\n", textfmt.Clip(fmt.Sprintf("… %s before this; /trajectory all shows them", textfmt.Plural(elided, "record")), o.width()))
	}
	for ti, t := range turns {
		if ti > 0 {
			sb.WriteString("\n")
		}
		fmt.Fprintf(&sb, "%s\n", textfmt.Clip(turnHeader(t), o.width()))
		for _, g := range t.Groups {
			shown := g.Records
			if len(keep) > 0 {
				shown = nil
				for _, r := range g.Records {
					if keep[r.Index] {
						shown = append(shown, r)
					}
				}
				if len(shown) == 0 {
					continue
				}
			}
			if note := groupHeader(g); note != "" {
				fmt.Fprintf(&sb, "  %s\n", textfmt.Clip(note, o.width()-2))
			}
			for _, r := range shown {
				sb.WriteString(Line(r, o.width()) + "\n")
			}
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

func turnHeader(t Turn) string {
	steps, tools := t.Calls()
	parts := []string{textfmt.Plural(steps, "step"), textfmt.Plural(tools, "tool call")}
	if span := t.Span(); span > 0 {
		parts = append(parts, textfmt.Duration(span))
	}
	if u := t.Usage(); !u.Empty() {
		parts = append(parts, tokenBrief(u))
	}
	name := "turn " + strconv.Itoa(t.Turn)
	if t.Turn == 0 {
		name = "before the first turn"
	}
	return name + " · " + strings.Join(parts, " · ")
}

// TurnHeading is the line Format puts over a turn, exported for views
// that build the ledger row by row instead of rendering it whole.
func TurnHeading(t Turn) string { return turnHeader(t) }

func groupHeader(g Group) string {
	if g.Step == 0 {
		return ""
	}
	note := g.Title
	if span := g.Span(); span > 0 {
		note += " · " + textfmt.Duration(span)
	}
	if tools := g.Tools(); tools != "" {
		note += " · " + tools
	}
	return note
}

// GroupHeading is the line Format puts over a step, exported for views
// that build the ledger row by row.
func GroupHeading(g Group) string { return groupHeader(g) }

// Line renders one record to one line of the given width.
func Line(r Record, width int) string {
	if width <= 0 {
		width = defaultWidth
	}
	left := fmt.Sprintf("%-*s %-*s", indexWidth, "#"+strconv.Itoa(r.Index), tagWidth, r.Kind.Tag())
	// On a narrow terminal the metrics are the first thing to go: what
	// the record did is worth more than what it cost, and a line that
	// wraps loses both.
	metrics := Metrics(r)
	room := width - utf8.RuneCountInString(left) - utf8.RuneCountInString(metrics) - metricGap
	if room < minContent {
		metrics, room = "", width-utf8.RuneCountInString(left)
	}
	if room < 1 {
		return textfmt.Clip(left+Content(r), width)
	}
	body := textfmt.Clip(Content(r), room)
	pad := width - utf8.RuneCountInString(left) - utf8.RuneCountInString(body) - utf8.RuneCountInString(metrics)
	if pad < 0 {
		pad = 0
	}
	if metrics == "" {
		return strings.TrimRight(left+body, " ")
	}
	return left + body + strings.Repeat(" ", pad) + metrics
}

// Content is the record's line without the gutter or the metrics: what
// it did, and for a tool what came back.
func Content(r Record) string {
	parts := []string{}
	if r.Text != "" {
		parts = append(parts, r.Text)
	}
	if r.Preview != "" {
		parts = append(parts, r.Preview)
	}
	body := strings.Join(parts, " · ")
	switch {
	case r.Denied:
		body += " → refused"
		if r.Result != "" {
			body += ": " + r.Result
		}
	case r.Running:
		body += " → …"
	case r.Kind == KindTool && r.Result == "":
		body += " → (no output)"
	case r.Result != "":
		arrow := " → "
		if r.IsError {
			arrow = " ✗ "
		}
		body += arrow + r.Result
	}
	return body
}

// Metrics is the right-hand column: tokens for a model call, a duration
// for anything that took time. A record with neither gets nothing rather
// than a row of dashes.
func Metrics(r Record) string {
	var parts []string
	if !r.Usage.Empty() {
		parts = append(parts, tokenBrief(r.Usage))
	}
	if r.Duration > 0 {
		parts = append(parts, textfmt.Duration(r.Duration))
	}
	return strings.Join(parts, " ")
}

// tokenBrief is "↑1.2k ⧉900 ↓30": sent, served from cache, generated.
// The cached share is separate because it is the cheap part, and a
// single input number that hides it says nothing useful.
func tokenBrief(u Usage) string {
	var parts []string
	if u.Input > 0 {
		parts = append(parts, "↑"+textfmt.Tokens(u.Input))
	}
	if u.CacheRead > 0 {
		parts = append(parts, "⧉"+textfmt.Tokens(u.CacheRead))
	}
	if u.Output > 0 {
		parts = append(parts, "↓"+textfmt.Tokens(u.Output))
	}
	return strings.Join(parts, " ")
}

// Turns is the folded view: one line per turn. A session with forty
// turns is a page here and forty pages in the ledger.
func Turns(l Ledger) string {
	if len(l.Turns) == 0 {
		return "no trajectory yet: no turn has run in this session"
	}
	var sb strings.Builder
	for _, t := range l.Turns {
		fmt.Fprintf(&sb, "%s\n", turnHeader(t))
		if ask := firstOfKind(t.Records(), KindUser); ask != nil {
			fmt.Fprintf(&sb, "    %s\n", textfmt.Clip(ask.Preview, 100))
		}
		fmt.Fprintf(&sb, "    #%d–#%d\n", t.Records()[0].Index, t.Records()[len(t.Records())-1].Index)
	}
	sb.WriteString("/trajectory turn <n> opens one · /trajectory <record> opens a record")
	return sb.String()
}

func firstOfKind(rs []Record, k Kind) *Record {
	for i := range rs {
		if rs[i].Kind == k {
			return &rs[i]
		}
	}
	return nil
}

// Inspect is the full record: everything the ledger had to squeeze onto
// one line, plus the timing and token detail there was never room for.
func Inspect(l Ledger, index int) string {
	r, ok := l.At(index)
	if !ok {
		return "no record #" + strconv.Itoa(index) + " · the ledger has " + textfmt.Plural(len(l.All), "record")
	}
	var sb strings.Builder
	where := "turn " + strconv.Itoa(r.Turn)
	if r.Step > 0 {
		where += " · step " + strconv.Itoa(r.Step)
	}
	fmt.Fprintf(&sb, "#%d  %s  %s  (log seq %d)", r.Index, r.Kind.Tag(), where, r.Seq)
	if r.Model != "" {
		sb.WriteString("  · " + r.Model)
	}
	sb.WriteString("\n")
	switch {
	case r.Denied:
		sb.WriteString("status   refused by the harness; nothing ran\n")
	case r.Running:
		sb.WriteString("status   dispatched, never answered (cancelled, or still running)\n")
	case r.IsError:
		sb.WriteString("status   failed\n")
	}
	if r.Tool != "" {
		fmt.Fprintf(&sb, "tool     %s", r.Tool)
		if r.CallID != "" {
			sb.WriteString("  (" + r.CallID + ")")
		}
		sb.WriteString("\n")
	}
	if r.Intent != "" {
		fmt.Fprintf(&sb, "intent   %s\n", r.Intent)
	}
	sb.WriteString(timingBlock(r))
	if !r.Usage.Empty() {
		sb.WriteString(usageBlock(r))
	}
	if r.Detail != "" {
		fmt.Fprintf(&sb, "\ninput\n%s\n", indent(clipBody(r.Detail)))
	}
	if r.Output != "" {
		label := "output"
		if r.Kind == KindMessage {
			label = "answer"
		}
		fmt.Fprintf(&sb, "\n%s\n%s\n", label, indent(clipBody(r.Output)))
	}
	return strings.TrimRight(sb.String(), "\n")
}

// bodyLimit is how much of a record's body the inspector prints. The
// whole thing can be megabytes of test output; a screen of it answers
// the question, and the log is still there for the rest.
const bodyLimit = 4000

func clipBody(s string) string {
	if len(s) <= bodyLimit {
		return s
	}
	return s[:bodyLimit] + fmt.Sprintf("\n… %s more", textfmt.Bytes(int64(len(s)-bodyLimit)))
}

func indent(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = "  " + l
	}
	return strings.Join(lines, "\n")
}

func timingBlock(r Record) string {
	var sb strings.Builder
	if !r.Started.IsZero() {
		fmt.Fprintf(&sb, "started  %s\n", r.Started.Format("15:04:05.000"))
	}
	if r.Duration > 0 {
		fmt.Fprintf(&sb, "took     %s\n", textfmt.Duration(r.Duration))
	}
	// The split is the diagnosis: a slow first token is the provider
	// queueing, a slow decode is a long answer, and one duration cannot
	// tell you which you have.
	if r.TTFT > 0 || r.Decode > 0 {
		fmt.Fprintf(&sb, "  first token after %s · generated for %s",
			textfmt.Duration(r.TTFT), textfmt.Duration(r.Decode))
		if tps, ok := r.Throughput(); ok {
			fmt.Fprintf(&sb, " · %.1f tok/s", tps)
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

func usageBlock(r Record) string {
	u := r.Usage
	var sb strings.Builder
	fmt.Fprintf(&sb, "tokens   %s in (%s of it cached", textfmt.Tokens(u.Input+u.CacheRead+u.CacheWrite), textfmt.Tokens(u.CacheRead))
	if u.CacheWrite > 0 {
		fmt.Fprintf(&sb, ", %s written to cache", textfmt.Tokens(u.CacheWrite))
	}
	fmt.Fprintf(&sb, ") · %s out\n", textfmt.Tokens(u.Output))
	c := r.Cumulative
	fmt.Fprintf(&sb, "  session so far: %s in · %s out", textfmt.Tokens(c.Input+c.CacheRead+c.CacheWrite), textfmt.Tokens(c.Output))
	if c.CostUSD > 0 {
		fmt.Fprintf(&sb, " · $%.4f reported", c.CostUSD)
	}
	sb.WriteString("\n")
	return sb.String()
}

// Search finds records matching every term, case-insensitively, over
// everything the record holds — arguments, results, the model's answer,
// the tool's name, the turn it was in — not just the line the ledger
// happened to render. A ledger you can only search by what fitted on
// screen is a ledger you cannot search.
func Search(l Ledger, query string) []Record {
	terms := strings.Fields(strings.ToLower(query))
	if len(terms) == 0 {
		return nil
	}
	var out []Record
	for _, r := range l.All {
		hay := haystack(r)
		ok := true
		for _, t := range terms {
			if !strings.Contains(hay, t) {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, r)
		}
	}
	return out
}

func haystack(r Record) string {
	var b strings.Builder
	for _, s := range []string{
		"turn " + strconv.Itoa(r.Turn), "step " + strconv.Itoa(r.Step), string(r.Kind),
		r.Text, r.Preview, r.Result, r.Tool, r.Intent, r.Model, r.CallID, r.Detail, r.Output,
	} {
		b.WriteString(s)
		b.WriteByte('\n')
	}
	return strings.ToLower(b.String())
}

// FormatSearch renders search hits the way the ledger renders records,
// so a hit and a line are the same thing.
func FormatSearch(hits []Record, query string, width int) string {
	if len(hits) == 0 {
		return "nothing in the trajectory matches " + strconv.Quote(query)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s matching %s\n", textfmt.Plural(len(hits), "record"), strconv.Quote(query))
	for _, r := range hits {
		sb.WriteString(Line(r, width) + "\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

// Slowest is the records that took longest, which is the other question
// a trajectory is opened to answer: not what happened, but what took so
// long. Model calls and tool calls are ranked together because they
// compete for the same wall clock.
func Slowest(l Ledger, n int) []Record {
	out := make([]Record, 0, len(l.All))
	for _, r := range l.All {
		if r.Duration > 0 {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Duration > out[j].Duration })
	if n > 0 && len(out) > n {
		out = out[:n]
	}
	return out
}

// FormatSlowest renders the ranking with each record's share of the
// total time accounted for.
func FormatSlowest(l Ledger, n, width int) string {
	rows := Slowest(l, n)
	if len(rows) == 0 {
		return "nothing in the trajectory has a duration yet"
	}
	var total time.Duration
	for _, r := range l.All {
		total += r.Duration
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "slowest records · %s accounted for\n", textfmt.Duration(total))
	for _, r := range rows {
		sb.WriteString(Line(r, width))
		if total > 0 {
			fmt.Fprintf(&sb, "  %d%%", int(r.Duration*100/total))
		}
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}
