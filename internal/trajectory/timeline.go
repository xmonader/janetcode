package trajectory

import (
	"sort"
	"strings"
)

// Mode is how the timeline maps records onto the track: the choice
// between "where in the order" and "where in the clock", crossed with
// "every record the same width" and "the width it actually took".
type Mode int

const (
	// ModeSequence lays records equal-width in ledger order. No timing
	// data is needed, so nothing is ever missing from the picture.
	ModeSequence Mode = iota
	// ModeDuration lays records at their recorded durations with the
	// idle gaps squeezed out: where the time went, proportionally.
	ModeDuration
	// ModeTime lays zero-width ticks at each record's wall-clock start:
	// when things happened, nothing more.
	ModeTime
	// ModeActual lays records at their recorded durations on the true
	// wall clock: the session's real shape, your think-time included.
	ModeActual
)

func (m Mode) String() string {
	switch m {
	case ModeDuration:
		return "duration"
	case ModeTime:
		return "time"
	case ModeActual:
		return "actual"
	default:
		return "sequence"
	}
}

// ParseMode reads the names the timeline endpoint and the TUI accept.
func ParseMode(s string) (Mode, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "sequence":
		return ModeSequence, true
	case "duration":
		return ModeDuration, true
	case "time":
		return ModeTime, true
	case "actual":
		return ModeActual, true
	}
	return 0, false
}

// Span is one ledger record projected into the timeline's domain. Start
// and End are in the mode's units — ordinal positions for ModeSequence,
// epoch milliseconds for the timed modes — not necessarily 0-based.
type Span struct {
	Index   int
	Kind    Kind
	Lane    int
	Start   float64
	End     float64
	Label   string
	IsError bool
	// TTFT is the fraction of a model span spent waiting for the first
	// token, 0..1, for the two-tone gradient that separates a slow
	// provider from a long answer. Zero on every other kind.
	TTFT float64
}

// TurnBoundary marks where a turn begins on the track, in the same
// domain as the spans.
type TurnBoundary struct {
	Turn int
	Time float64
}

// Timeline is the whole track: every span, where each turn starts, and
// the domain the spans live in.
type Timeline struct {
	Spans    []Span
	Boundary []TurnBoundary
	Start    float64
	End      float64
}

// LaneFor sorts a kind onto its fixed lane: input on 0, the model on 1,
// tools on 2. The lanes are few on purpose — they are a legend the
// reader holds while scanning the track.
func LaneFor(k Kind) int {
	switch k {
	case KindTool:
		return 2
	case KindMessage, KindCompacted, KindVerdict:
		return 1
	default:
		return 0
	}
}

// TimelineOf projects the ledger onto the track in the given mode. The
// second return is false when there is nothing to draw: an empty ledger
// in any mode, or a ledger whose records carry no observed times in a
// timed mode — nothing is computed that was not observed, so a timed
// projection of an untimed ledger is "no timeline", not a guess.
func TimelineOf(l Ledger, mode Mode) (Timeline, bool) {
	if len(l.All) == 0 {
		return Timeline{}, false
	}
	if mode == ModeSequence {
		return sequenceTimeline(l), true
	}
	return timedTimeline(l, mode == ModeActual || mode == ModeDuration, mode == ModeDuration)
}

func sequenceTimeline(l Ledger) Timeline {
	t := Timeline{}
	for _, turn := range l.Turns {
		if len(turn.Records()) == 0 {
			continue
		}
		t.Boundary = append(t.Boundary, TurnBoundary{Turn: turn.Turn, Time: float64(len(t.Spans))})
		for _, r := range turn.Records() {
			t.Spans = append(t.Spans, Span{
				Index:   r.Index,
				Kind:    r.Kind,
				Lane:    LaneFor(r.Kind),
				Start:   float64(len(t.Spans)),
				End:     float64(len(t.Spans) + 1),
				Label:   Content(r),
				IsError: r.IsError,
				TTFT:    ttftFraction(r),
			})
		}
	}
	t.Start, t.End = 0, float64(len(t.Spans))
	return t
}

// timedTimeline is the shared projection under the three clock modes.
// actualDuration picks real durations over zero-width ticks;
// compressIdle squeezes the gaps out of the domain.
func timedTimeline(l Ledger, actualDuration, compressIdle bool) (Timeline, bool) {
	type turnSpans struct {
		turn  int
		spans []Span
	}
	var turns []turnSpans
	var all []Span
	for _, turn := range l.Turns {
		var spans []Span
		for _, r := range turn.Records() {
			if r.Started.IsZero() {
				continue
			}
			start := float64(r.Started.UnixMilli())
			end := start
			if actualDuration {
				end = start + float64(max(r.Duration, 0))/1e6
			}
			spans = append(spans, Span{
				Index:   r.Index,
				Kind:    r.Kind,
				Lane:    LaneFor(r.Kind),
				Start:   start,
				End:     end,
				Label:   Content(r),
				IsError: r.IsError,
				TTFT:    ttftFraction(r),
			})
		}
		if len(spans) == 0 {
			continue
		}
		turns = append(turns, turnSpans{turn.Turn, spans})
		all = append(all, spans...)
	}
	if len(all) == 0 {
		return Timeline{}, false
	}

	// The idle removed before each span is computed on spans sorted by
	// start: a gap counts only past what is already covered, so spans
	// that overlap — parallel tool calls — never create negative idle.
	byStart := make([]Span, len(all))
	copy(byStart, all)
	sort.SliceStable(byStart, func(i, j int) bool {
		return byStart[i].Start < byStart[j].Start ||
			byStart[i].Start == byStart[j].Start && byStart[i].End < byStart[j].End
	})
	removedByIndex := map[int]float64{}
	var removed, covered float64
	haveCovered := false
	for _, s := range byStart {
		if compressIdle && haveCovered && s.Start > covered {
			removed += s.Start - covered
		}
		removedByIndex[s.Index] = removed
		if !haveCovered || s.End > covered {
			covered = s.End
			haveCovered = true
		}
	}

	t := Timeline{}
	for _, turn := range turns {
		first := 0.0
		for i, s := range turn.spans {
			offset := removedByIndex[s.Index]
			s.Start -= offset
			s.End -= offset
			if i == 0 || s.Start < first {
				first = s.Start
			}
			t.Spans = append(t.Spans, s)
		}
		t.Boundary = append(t.Boundary, TurnBoundary{Turn: turn.turn, Time: first})
	}
	t.Start = t.Spans[0].Start
	t.End = t.Spans[0].End
	for _, s := range t.Spans[1:] {
		if s.Start < t.Start {
			t.Start = s.Start
		}
		if s.End > t.End {
			t.End = s.End
		}
	}
	return t, true
}

// FocusIndexes is the brush filter: the indexes of the records whose
// spans touch the inclusive interval. A dim-filter, not a hide-filter —
// the reader keeps the surrounding context.
func FocusIndexes(t Timeline, start, end float64) []int {
	var out []int
	for _, s := range t.Spans {
		if s.Start <= end && s.End >= start {
			out = append(out, s.Index)
		}
	}
	return out
}

// ttftFraction is the waiting share of a model span's decode window,
// what the gradient paints light before the dark generation share.
func ttftFraction(r Record) float64 {
	if r.Kind != KindMessage || r.TTFT <= 0 || r.Decode <= 0 {
		return 0
	}
	if f := float64(r.TTFT) / float64(r.TTFT+r.Decode); f > 1 {
		return 1
	} else if f < 0 {
		return 0
	} else {
		return f
	}
}
