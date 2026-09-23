package webui

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/present"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/textfmt"
	"github.com/xmonader/janetcode/internal/trajectory"
)

// The trajectory API serves the folded ledger, not the raw event log:
// a tool call and its result are one record, a step's records group
// under it, and every record carries the timing split (ttft/decode) and
// the running usage total. The raw log stays available at /raw for when
// the ledger itself is what looks wrong.

type trajUsage struct {
	In         int     `json:"in"`
	CacheRead  int     `json:"cache_read"`
	CacheWrite int     `json:"cache_write"`
	Out        int     `json:"out"`
	CostUSD    float64 `json:"cost_usd,omitempty"`
}

type trajRecord struct {
	Index    int        `json:"index"`
	Seq      int        `json:"seq"`
	Turn     int        `json:"turn"`
	Step     int        `json:"step"`
	Kind     string     `json:"kind"`
	Tag      string     `json:"tag"`
	Text     string     `json:"text,omitempty"`
	Preview  string     `json:"preview,omitempty"`
	Result   string     `json:"result,omitempty"`
	Error    bool       `json:"error,omitempty"`
	Denied   bool       `json:"denied,omitempty"`
	Running  bool       `json:"running,omitempty"`
	Tool     string     `json:"tool,omitempty"`
	Intent   string     `json:"intent,omitempty"`
	Model    string     `json:"model,omitempty"`
	Slot     string     `json:"slot,omitempty"`
	Route    string     `json:"route,omitempty"`
	RouteWhy string     `json:"route_why,omitempty"`
	Started  int64      `json:"started_ms,omitempty"`
	Duration int64      `json:"duration_ms,omitempty"`
	TTFT     int64      `json:"ttft_ms,omitempty"`
	Decode   int64      `json:"decode_ms,omitempty"`
	TokPerS  float64    `json:"tok_per_s,omitempty"`
	Usage    *trajUsage `json:"usage,omitempty"`
	Total    *trajUsage `json:"total,omitempty"` // the session's bill up to and including this record
	// Input and Output are the record's full body, only on the detail
	// response; the list carries previews.
	Input  string `json:"input,omitempty"`
	Output string `json:"output,omitempty"`
}

type trajTurn struct {
	Turn    int       `json:"turn"`
	Steps   int       `json:"steps"`
	Tools   int       `json:"tools"`
	SpanMs  int64     `json:"span_ms,omitempty"`
	Usage   trajUsage `json:"usage"`
	First   int       `json:"first"`
	Last    int       `json:"last"`
	Heading string    `json:"heading"`
}

func (s *server) trajectoryAPI(w http.ResponseWriter, r *http.Request) {
	if n, err := strconv.Atoi(r.URL.Query().Get("n")); err == nil && n > 0 {
		s.trajectoryDetail(w, n)
		return
	}
	l := s.trajLedger()
	out := map[string]any{
		"records": trajRecords(l.All, false),
		"turns":   trajTurns(l),
		"usage":   trajUsageOf(l.Usage()),
	}
	writeJSON(w, out)
}

// trajLedger folds the session log, or an empty ledger without a session.
func (s *server) trajLedger() trajectory.Ledger {
	if s.sess() == nil {
		return trajectory.Ledger{}
	}
	return trajectory.Fold(s.sess().Events())
}

func trajRecords(rs []trajectory.Record, full bool) []trajRecord {
	out := make([]trajRecord, 0, len(rs))
	for _, r := range rs {
		rec := trajRecord{
			Index:    r.Index,
			Seq:      r.Seq,
			Turn:     r.Turn,
			Step:     r.Step,
			Kind:     string(r.Kind),
			Tag:      r.Kind.Tag(),
			Text:     r.Text,
			Preview:  r.Preview,
			Result:   r.Result,
			Error:    r.IsError,
			Denied:   r.Denied,
			Running:  r.Running,
			Tool:     r.Tool,
			Intent:   r.Intent,
			Model:    r.Model,
			Slot:     r.Slot,
			Route:    r.Route,
			RouteWhy: r.RouteWhy,
		}
		if !r.Started.IsZero() {
			rec.Started = r.Started.UnixMilli()
		}
		if r.Duration > 0 {
			rec.Duration = r.Duration.Milliseconds()
		}
		if r.TTFT > 0 {
			rec.TTFT = r.TTFT.Milliseconds()
		}
		if r.Decode > 0 {
			rec.Decode = r.Decode.Milliseconds()
		}
		if tps, ok := r.Throughput(); ok {
			rec.TokPerS = tps
		}
		if !r.Usage.Empty() {
			rec.Usage = trajUsageOf(r.Usage)
		}
		if !r.Cumulative.Empty() {
			rec.Total = trajUsageOf(r.Cumulative)
		}
		if full {
			rec.Input = r.Detail
			rec.Output = r.Output
		}
		out = append(out, rec)
	}
	return out
}

func trajUsageOf(u trajectory.Usage) *trajUsage {
	return &trajUsage{
		In:         u.Input,
		CacheRead:  u.CacheRead,
		CacheWrite: u.CacheWrite,
		Out:        u.Output,
		CostUSD:    u.CostUSD,
	}
}

func trajTurns(l trajectory.Ledger) []trajTurn {
	out := make([]trajTurn, 0, len(l.Turns))
	for _, t := range l.Turns {
		steps, tools := t.Calls()
		rs := t.Records()
		out = append(out, trajTurn{
			Turn:    t.Turn,
			Steps:   steps,
			Tools:   tools,
			SpanMs:  t.Span().Milliseconds(),
			Usage:   *trajUsageOf(t.Usage()),
			First:   rs[0].Index,
			Last:    rs[len(rs)-1].Index,
			Heading: trajectory.TurnHeading(t),
		})
	}
	return out
}

func (s *server) trajectoryDetail(w http.ResponseWriter, n int) {
	l := s.trajLedger()
	r, ok := l.At(n)
	if !ok {
		http.Error(w, "no record with that index", http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]any{"record": trajRecords([]trajectory.Record{r}, true)[0]})
}

// trajectoryTimelineAPI projects the ledger for the span chart. The
// mode names are the trajectory package's four: sequence, duration,
// time, actual.
func (s *server) trajectoryTimelineAPI(w http.ResponseWriter, r *http.Request) {
	mode, ok := trajectory.ParseMode(r.URL.Query().Get("mode"))
	if !ok {
		http.Error(w, "mode is one of sequence, duration, time, actual", http.StatusBadRequest)
		return
	}
	l := s.trajLedger()
	out := map[string]any{"mode": mode.String(), "spans": []trajSpan{}, "turns": []trajBoundary{}}
	if tl, ok := trajectory.TimelineOf(l, mode); ok {
		spans := make([]trajSpan, 0, len(tl.Spans))
		for _, sp := range tl.Spans {
			spans = append(spans, trajSpan{
				Index: sp.Index,
				Kind:  string(sp.Kind),
				Lane:  sp.Lane,
				Start: sp.Start,
				End:   sp.End,
				Label: sp.Label,
				Error: sp.IsError,
				TTFT:  sp.TTFT,
			})
		}
		bounds := make([]trajBoundary, 0, len(tl.Boundary))
		for _, b := range tl.Boundary {
			bounds = append(bounds, trajBoundary{Turn: b.Turn, Time: b.Time})
		}
		out["spans"] = spans
		out["turns"] = bounds
		out["start"] = tl.Start
		out["end"] = tl.End
	}
	writeJSON(w, out)
}

type trajSpan struct {
	Index int     `json:"index"`
	Kind  string  `json:"kind"`
	Lane  int     `json:"lane"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Label string  `json:"label"`
	Error bool    `json:"error,omitempty"`
	TTFT  float64 `json:"ttft,omitempty"`
}

type trajBoundary struct {
	Turn int     `json:"turn"`
	Time float64 `json:"time"`
}

// trajectoryRawAPI is the unfolded log, one row per event as it is on
// disk, for when the ledger itself is what looks wrong.
func (s *server) trajectoryRawAPI(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{"events": s.trajectoryRows()})
}

func (s *server) trajectory(arg string) {
	if s.sess() == nil {
		s.errf("no session")
		return
	}
	ev := s.sess().Events()
	args := strings.Fields(arg)
	out, ok := trajectory.Command(ev, args, trajSheetWidth)
	if !ok {
		s.errf("%s", out)
		return
	}
	title := "trajectory"
	if arg != "" {
		title += " · " + arg
	}
	s.sheet(title, out)
}

// trajSheetWidth is how wide a sheet in the web UI is assumed to be.
// The browser reflows text, but the ledger's columns only line up at a
// fixed width, so one is chosen rather than guessed at per client.
const trajSheetWidth = 110

type trajRow struct {
	Seq     int    `json:"seq"`
	Turn    int    `json:"turn"`
	Step    int    `json:"step"`
	Type    string `json:"type"`
	Kind    string `json:"kind"`
	Name    string `json:"name,omitempty"`
	Dur     string `json:"dur,omitempty"`
	DurMs   int64  `json:"dur_ms,omitempty"`
	Surface string `json:"surface"`
	Preview string `json:"preview,omitempty"`
	Error   bool   `json:"error,omitempty"`
	At      string `json:"at,omitempty"`
}

func (s *server) trajectoryRows() []trajRow {
	if s.sess() == nil {
		return []trajRow{}
	}
	ev := s.sess().Events()
	hidden := session.Shadowed(ev)
	out := make([]trajRow, 0, len(ev))
	for _, e := range ev {
		row := trajRow{
			Seq:     e.Seq,
			Turn:    e.Turn,
			Step:    e.Step,
			Type:    e.Type,
			Kind:    trajKind(e.Type),
			Name:    e.Name,
			Surface: string(session.ClassifyWith(hidden, e)),
			Preview: trajPreview(e),
			Error:   e.IsError,
		}
		if e.DurationMs > 0 {
			row.DurMs = e.DurationMs
			row.Dur = textfmt.Duration(time.Duration(e.DurationMs) * time.Millisecond)
		}
		if !e.Time.IsZero() {
			row.At = e.Time.UTC().Format(time.RFC3339)
		}
		out = append(out, row)
	}
	return out
}

func trajKind(t string) string {
	switch t {
	case session.TypeUserMessage:
		return "user"
	case session.TypeAssistant:
		return "assistant"
	case session.TypeToolCall:
		return "tool"
	case session.TypeToolResult:
		return "result"
	case session.TypeCompactStart, session.TypeCompactSummary, session.TypeCompactEnd:
		return "compact"
	case session.TypeTurnStart, session.TypeTurnEnd:
		return "turn"
	case session.TypeCritic:
		return "critic"
	case session.TypeTodoWrite:
		return "plan"
	default:
		if strings.HasPrefix(t, "goal/") {
			return "goal"
		}
		return "other"
	}
}

func trajPreview(e session.Event) string {
	if e.Type == session.TypeToolCall {
		s := e.Name
		if e.Intent != "" {
			s += " · " + e.Intent
		} else if e.Arguments != "" {
			s += " · " + present.ToolSummary(e.Name, e.Arguments, 80, "")
		}
		return s
	}
	if e.Output != "" {
		return textfmt.Clip(textfmt.OneLine(e.Output), 120)
	}
	if e.Message != nil {
		return textfmt.Clip(textfmt.OneLine(e.Message.Text()), 120)
	}
	if e.Reason != "" {
		return textfmt.Clip(textfmt.OneLine(e.Reason), 120)
	}
	return ""
}
