package trajectory

import (
	"slices"
	"testing"
	"time"
)

// epoch is a fixed, round wall-clock base so the timed projections can
// be asserted to the millisecond.
var epoch = time.UnixMilli(1_700_000_000_000)

func timedRecord(index, turn, step int, k Kind, atMS int, durMS int) Record {
	return Record{
		Index: index, Turn: turn, Step: step, Kind: k,
		Started:  epoch.Add(time.Duration(atMS) * time.Millisecond),
		Duration: time.Duration(durMS) * time.Millisecond,
	}
}

// Two turns: a model call, two overlapping tool calls, an idle gap you
// could drive a turn through, then another model call. One record in
// the second turn carries no observed time at all.
func timelineLedger() Ledger {
	recs := []Record{
		timedRecord(1, 1, 0, KindUser, 0, 0),
		timedRecord(2, 1, 1, KindMessage, 100, 1000),
		timedRecord(3, 1, 1, KindTool, 1100, 2000),
		timedRecord(4, 1, 1, KindTool, 1150, 1000),
		timedRecord(5, 2, 0, KindUser, 10000, 0),
		timedRecord(6, 2, 2, KindMessage, 10100, 500),
		{Index: 7, Turn: 2, Step: 2, Kind: KindUser}, // no clock on this one
	}
	recs[1].TTFT = 300 * time.Millisecond
	recs[1].Decode = 700 * time.Millisecond
	recs[5].TTFT = 100 * time.Millisecond
	recs[5].Decode = 400 * time.Millisecond
	return Ledger{
		All: recs,
		Turns: []Turn{
			{Turn: 1, Groups: []Group{{Title: "Message", Records: recs[:1]}, {Title: "Step 1", Records: recs[1:4]}}},
			{Turn: 2, Groups: []Group{{Title: "Message", Records: recs[4:5]}, {Title: "Step 2", Records: recs[5:]}}},
		},
	}
}

func span(t Timeline, index int) Span {
	for _, s := range t.Spans {
		if s.Index == index {
			return s
		}
	}
	return Span{Index: -1}
}

func TestTimelineSequenceIsOneUnitPerRecord(t *testing.T) {
	tl, ok := TimelineOf(timelineLedger(), ModeSequence)
	if !ok {
		t.Fatal("sequence found no timeline")
	}
	if len(tl.Spans) != 7 {
		t.Fatalf("sequence dropped the untimed record: %d spans", len(tl.Spans))
	}
	for i, s := range tl.Spans {
		if s.Start != float64(i) || s.End != float64(i+1) {
			t.Fatalf("span %d at [%v, %v), want [%d, %d)", s.Index, s.Start, s.End, i, i+1)
		}
	}
	if tl.Start != 0 || tl.End != 7 {
		t.Fatalf("domain = [%v, %v], want [0, 7)", tl.Start, tl.End)
	}
	want := []TurnBoundary{{Turn: 1, Time: 0}, {Turn: 2, Time: 4}}
	if !slices.Equal(tl.Boundary, want) {
		t.Fatalf("boundaries = %v, want %v", tl.Boundary, want)
	}
	if lanes := []int{span(tl, 1).Lane, span(tl, 2).Lane, span(tl, 3).Lane}; lanes[0] != 0 || lanes[1] != 1 || lanes[2] != 2 {
		t.Fatalf("lanes = %v, want input/model/tool", lanes)
	}
}

func TestTimelineActualKeepsTheClock(t *testing.T) {
	tl, ok := TimelineOf(timelineLedger(), ModeActual)
	if !ok {
		t.Fatal("actual found no timeline")
	}
	if len(tl.Spans) != 6 {
		t.Fatalf("the untimed record should be absent, %d spans", len(tl.Spans))
	}
	base := float64(epoch.UnixMilli())
	cases := map[int][2]float64{2: {base + 100, base + 1100}, 3: {base + 1100, base + 3100},
		4: {base + 1150, base + 2150}, 6: {base + 10100, base + 10600}}
	for index, want := range cases {
		if s := span(tl, index); s.Start != want[0] || s.End != want[1] {
			t.Fatalf("span %d = [%v, %v], want [%v, %v]", index, s.Start, s.End, want[0], want[1])
		}
	}
	if tl.Start != base || tl.End != base+10600 {
		t.Fatalf("domain = [%v, %v], want [%v, %v]", tl.Start, tl.End, base, base+10600)
	}
	if got := tl.Boundary[1]; got.Turn != 2 || got.Time != base+10000 {
		t.Fatalf("turn 2 boundary = %+v, want time %v", got, base+10000)
	}
}

func TestTimelineDurationSqueezesTheIdleOut(t *testing.T) {
	tl, ok := TimelineOf(timelineLedger(), ModeDuration)
	if !ok {
		t.Fatal("duration found no timeline")
	}
	// The domain keeps the epoch origin — only the idle is subtracted.
	base := float64(epoch.UnixMilli())
	cases := map[int][2]float64{1: {base, base}, 2: {base, base + 1000}, 3: {base + 1000, base + 3000},
		4: {base + 1050, base + 2050}, 5: {base + 3000, base + 3000}, 6: {base + 3000, base + 3500}}
	for index, want := range cases {
		if s := span(tl, index); s.Start != want[0] || s.End != want[1] {
			t.Fatalf("span %d = [%v, %v], want [%v, %v]", index, s.Start, s.End, want[0], want[1])
		}
	}
	if tl.End != base+3500 {
		t.Fatalf("domain ends at %v, want %v (10600ms clock minus 7100ms idle)", tl.End, base+3500)
	}
	// The turn boundary rides the first span of its turn in the
	// compressed domain, not its uncompressed wall-clock position.
	if got := tl.Boundary[1]; got.Turn != 2 || got.Time != base+3000 {
		t.Fatalf("turn 2 boundary = %+v, want time %v", got, base+3000)
	}
}

func TestTimelineDurationTreatsOverlapsAsCovered(t *testing.T) {
	// The second tool call starts while the first is still running:
	// the overlap is not idle and must not be squeezed.
	tl, _ := TimelineOf(timelineLedger(), ModeDuration)
	if s := span(tl, 4); s.Start != float64(epoch.UnixMilli())+1050 {
		t.Fatalf("span 4 starts at %v, want epoch+1050 — the overlap was counted as idle", s.Start)
	}
}

func TestTimelineTimeIsTicksAtTheStarts(t *testing.T) {
	tl, ok := TimelineOf(timelineLedger(), ModeTime)
	if !ok {
		t.Fatal("time found no timeline")
	}
	base := float64(epoch.UnixMilli())
	for index, at := range map[int]float64{2: base + 100, 3: base + 1100, 6: base + 10100} {
		if s := span(tl, index); s.Start != at || s.End != at {
			t.Fatalf("span %d = [%v, %v], want a tick at %v", index, s.Start, s.End, at)
		}
	}
	if tl.End != base+10100 {
		t.Fatalf("domain ends at %v, want %v", tl.End, base+10100)
	}
}

func TestTimelineIsAbsentWhenNothingIsTimed(t *testing.T) {
	l := Ledger{All: []Record{{Index: 1, Kind: KindUser}}}
	if _, ok := TimelineOf(l, ModeActual); ok {
		t.Fatal("a timed projection of an untimed ledger should be absent, not a guess")
	}
	if _, ok := TimelineOf(Ledger{}, ModeSequence); ok {
		t.Fatal("an empty ledger has no timeline in any mode")
	}
}

func TestFocusIndexesIsInclusiveBothWays(t *testing.T) {
	tl, _ := TimelineOf(timelineLedger(), ModeDuration)
	base := float64(epoch.UnixMilli())
	got := FocusIndexes(tl, base+900, base+1100)
	if !slices.Equal(got, []int{2, 3, 4}) {
		t.Fatalf("focus = %v, want [2 3 4] — boundary touches count", got)
	}
	if got := FocusIndexes(tl, base+2900, base+3000); !slices.Equal(got, []int{3, 5, 6}) {
		t.Fatalf("focus = %v, want [3 5 6]", got)
	}
}

func TestModelSpansCarryTheWaitingShare(t *testing.T) {
	tl, _ := TimelineOf(timelineLedger(), ModeSequence)
	if f := span(tl, 2).TTFT; f != 0.3 {
		t.Fatalf("ttft fraction = %v, want 0.3", f)
	}
	if f := span(tl, 6).TTFT; f != 0.2 {
		t.Fatalf("ttft fraction = %v, want 0.2", f)
	}
	if f := span(tl, 3).TTFT; f != 0 {
		t.Fatalf("a tool span wears no gradient: %v", f)
	}
}

func TestParseModeRoundTrips(t *testing.T) {
	for _, name := range []string{"sequence", "duration", "time", "actual"} {
		m, ok := ParseMode(name)
		if !ok || m.String() != name {
			t.Fatalf("ParseMode(%q) = %v, %v", name, m, ok)
		}
	}
	if _, ok := ParseMode("sideways"); ok {
		t.Fatal("sideways is not a mode")
	}
}

// A folded session log projects without any hand-built records: the
// step-start fallback the fold uses for model calls is all the timing
// the timeline needs.
func TestTimelineOverAFoldedLog(t *testing.T) {
	tl, ok := TimelineOf(Fold(sample(epoch)), ModeDuration)
	if !ok {
		t.Fatal("a folded log found no timeline")
	}
	if len(tl.Spans) != 5 { // user, model, two tools, model
		t.Fatalf("spans = %d, want 5", len(tl.Spans))
	}
	var model Span
	for _, s := range tl.Spans {
		if s.Kind == KindMessage && s.TTFT > 0 {
			model = s
		}
	}
	if model.Index == 0 {
		t.Fatalf("the model span lost its waiting share: %+v", tl.Spans)
	}
	if got := FocusIndexes(tl, model.Start, model.End); !slices.Contains(got, model.Index) {
		t.Fatalf("a span does not even focus itself: %v vs %+v", got, model)
	}
}
