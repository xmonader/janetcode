package webui

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/textfmt"
	"github.com/xmonader/janetcode/internal/trajectory"
)

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

func (s *server) trajectoryAPI(w http.ResponseWriter, r *http.Request) {
	if n, err := strconv.Atoi(r.URL.Query().Get("n")); err == nil && n > 0 {
		s.trajectoryDetail(w, n)
		return
	}
	writeJSON(w, map[string]any{"events": s.trajectoryRows()})
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

func (s *server) trajectoryDetail(w http.ResponseWriter, n int) {
	if s.sess() == nil {
		http.Error(w, "no session", http.StatusNotFound)
		return
	}
	ev := s.sess().Events()
	for _, e := range ev {
		if e.Seq != n {
			continue
		}
		body := e.Output
		if body == "" && e.Message != nil {
			body = e.Message.Text()
		}
		item := map[string]any{
			"seq":     e.Seq,
			"type":    e.Type,
			"kind":    trajKind(e.Type),
			"turn":    e.Turn,
			"step":    e.Step,
			"name":    e.Name,
			"surface": string(session.Classify(ev, e)),
			"error":   e.IsError,
			"preview": textfmt.Clip(body, 4000),
		}
		if e.DurationMs > 0 {
			item["dur"] = textfmt.Duration(time.Duration(e.DurationMs) * time.Millisecond)
		}
		if e.Intent != "" {
			item["intent"] = e.Intent
		}
		if e.Arguments != "" {
			item["arguments"] = textfmt.Clip(e.Arguments, 2000)
		}
		if e.Usage != nil {
			item["usage"] = map[string]int{
				"in":    e.Usage.InputTokens,
				"cache": e.Usage.CacheReadTokens,
				"out":   e.Usage.OutputTokens,
			}
		}
		writeJSON(w, item)
		return
	}
	http.Error(w, "unknown event", http.StatusNotFound)
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

// trajKind maps an event type to the short kind name the trajectory
// view colors by.
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
			s += " · " + summarizeInput(e.Name, e.Arguments, 80, "")
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
