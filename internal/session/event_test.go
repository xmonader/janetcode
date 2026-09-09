package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/xmonader/janetcode/internal/llm"
)

func TestFoldOrdersReplacementsInPlace(t *testing.T) {
	u := llm.TextMessage(llm.RoleUser, "first")
	a := llm.TextMessage(llm.RoleAssistant, "second")
	later := llm.TextMessage(llm.RoleUser, "third")
	pruned := llm.TextMessage(llm.RoleAssistant, "second (pruned)")
	events := []Event{
		{Seq: 1, Type: TypeUserMessage, Surface: true, Message: &u},
		{Seq: 2, Type: TypeAssistant, Surface: true, Message: &a},
		{Seq: 3, Type: TypeUserMessage, Surface: true, Message: &later},
		// A message rewritten in place is logged last but stands where the
		// message it replaces stood.
		{Seq: 4, Type: TypeAssistant, Surface: true, SurfaceOp: SurfaceOpReplace, Replaces: []int{2}, Message: &pruned},
	}
	got := FoldSurface(events)
	if len(got) != 3 || got[0].Text() != "first" || got[1].Text() != "second (pruned)" || got[2].Text() != "third" {
		t.Fatalf("fold = %+v", got)
	}
	if seqs := CurrentSeqs(events); len(seqs) != 3 || seqs[1] != 4 {
		t.Fatalf("CurrentSeqs = %v", seqs)
	}
	// Replacing a replacement keeps the same slot.
	again := llm.TextMessage(llm.RoleAssistant, "second (condensed)")
	events = append(events, Event{Seq: 5, Type: TypeAssistant, Surface: true, SurfaceOp: SurfaceOpReplace, Replaces: []int{4}, Message: &again})
	got = FoldSurface(events)
	if len(got) != 3 || got[1].Text() != "second (condensed)" {
		t.Fatalf("second fold = %+v", got)
	}
}

func TestAppendIsAppendOnly(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir, "/w", "p", "m")
	if err != nil {
		t.Fatal(err)
	}
	u := llm.TextMessage(llm.RoleUser, "hi")
	if _, err := s.Append(Event{Type: TypeTurnStart, Turn: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(Event{Type: TypeUserMessage, Turn: 1, Surface: true, Message: &u}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(Event{Type: TypeTurnEnd, Turn: 1, Reason: "end_turn"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	n := strings.Count(string(raw), "\n")
	if _, err := s.Append(Event{Type: TypeNotice, Output: "nudge"}); err != nil {
		t.Fatal(err)
	}
	raw2, _ := os.ReadFile(s.Path())
	if strings.Count(string(raw2), "\n") != n+1 {
		t.Fatalf("append rewrote the file: %d -> %d lines", n, strings.Count(string(raw2), "\n"))
	}
	re, msgs, err := Open(dir, s.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(re.Events()) != 4 || re.Events()[0].Seq != 1 || re.Events()[3].Type != TypeNotice {
		t.Fatalf("events = %+v", re.Events())
	}
	if len(msgs) != 1 || msgs[0].Text() != "hi" {
		t.Fatalf("surface = %+v", msgs)
	}
}

func TestFoldSurfaceSkipsShadowed(t *testing.T) {
	u := llm.TextMessage(llm.RoleUser, "long")
	a := llm.TextMessage(llm.RoleAssistant, "ok")
	tool := llm.ToolResultMessage(llm.ToolResult("t1", "lots of output", false))
	sum := llm.TextMessage(llm.RoleUser, "summary")
	events := []Event{
		{Seq: 1, Type: TypeUserMessage, Surface: true, Message: &u},
		{Seq: 2, Type: TypeAssistant, Surface: true, Message: &a},
		{Seq: 3, Type: TypeToolResult, Surface: true, Message: &tool, CallID: "t1", Output: "lots of output"},
		{Seq: 4, Type: TypeCompactStart},
		{Seq: 5, Type: TypeCompactSummary, Shadowed: []int{1, 2, 3}},
		{Seq: 6, Type: TypeUserMessage, Surface: true, SurfaceOp: SurfaceOpReplace, Replaces: []int{1, 2, 3}, Message: &sum},
		{Seq: 7, Type: TypeCompactEnd},
	}
	got := FoldSurface(events)
	if len(got) != 1 || got[0].Text() != "summary" {
		t.Fatalf("fold = %+v", got)
	}
	if Classify(events, events[2]) != SurfaceShadowed {
		t.Fatalf("tool should be shadowed, got %s", Classify(events, events[2]))
	}
	if Classify(events, events[5]) != SurfaceCurrent {
		t.Fatalf("summary should be current")
	}
	if Classify(events, events[4]) != SurfaceLogOnly {
		t.Fatalf("compaction summary is log-only")
	}
}

func TestUnknownEventTypeSkipped(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(dir, "/w", "p", "m")
	u := llm.TextMessage(llm.RoleUser, "hi")
	if _, err := s.Append(Event{Type: TypeUserMessage, Surface: true, Message: &u}); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(s.Path(), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"type":"event","seq":99,"t":"future/thing","extra":{"x":1}}` + "\n")
	f.Close()
	re, msgs, err := Open(dir, s.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Text() != "hi" {
		t.Fatalf("msgs = %+v", msgs)
	}
	found := false
	for _, e := range re.Events() {
		if e.Type == "future/thing" {
			found = true
			if e.Seq != 99 {
				t.Fatalf("seq = %d", e.Seq)
			}
		}
	}
	if !found {
		t.Fatal("unknown event dropped instead of kept")
	}
}

func TestCompactionStaysInTheSameFile(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(dir, "/w", "p", "m")
	u := llm.TextMessage(llm.RoleUser, "a")
	a := llm.TextMessage(llm.RoleAssistant, "b")
	if _, err := s.Append(Event{Type: TypeUserMessage, Surface: true, Message: &u}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(Event{Type: TypeAssistant, Surface: true, Message: &a}); err != nil {
		t.Fatal(err)
	}
	sum := llm.TextMessage(llm.RoleUser, "summary")
	if _, err := s.Append(Event{Type: TypeCompactSummary, Shadowed: []int{1, 2}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(Event{Type: TypeUserMessage, Surface: true, SurfaceOp: SurfaceOpReplace, Replaces: []int{1, 2}, Message: &sum}); err != nil {
		t.Fatal(err)
	}
	re, msgs, err := Open(dir, s.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Text() != "summary" {
		t.Fatalf("surface after compact = %+v", msgs)
	}
	if len(re.Events()) < 4 {
		t.Fatalf("lost pre-compact events: %d", len(re.Events()))
	}
	if Classify(re.Events(), re.Events()[0]) != SurfaceShadowed {
		t.Fatalf("first user should stay shadowed in the same file")
	}
}

func TestPinsRewriteKeepsEvents(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(dir, "/w", "p", "m")
	u := llm.TextMessage(llm.RoleUser, "hi")
	if _, err := s.Append(Event{Type: TypeUserMessage, Surface: true, Message: &u}); err != nil {
		t.Fatal(err)
	}
	if err := s.SavePins([]string{"pin"}); err != nil {
		t.Fatal(err)
	}
	re, msgs, err := Open(dir, s.Meta.ID)
	if err != nil || len(re.Meta.Pins) != 1 || len(msgs) != 1 || len(re.Events()) != 1 {
		t.Fatalf("pins=%v msgs=%d events=%d err=%v", re.Meta.Pins, len(msgs), len(re.Events()), err)
	}
}

func TestFoldPlanMode(t *testing.T) {
	on, off := true, false
	events := []Event{
		{Type: TypePlanMode, Active: &on},
		{Type: TypeUserMessage, Surface: true, Message: ptrMsg(llm.TextMessage(llm.RoleUser, "x"))},
		{Type: TypePlanMode, Active: &off},
	}
	if FoldPlanMode(events) {
		t.Fatal("last plan/mode is off")
	}
	if !FoldPlanMode(events[:1]) {
		t.Fatal("first plan/mode is on")
	}
}

func TestMemoryLogAssignsSeq(t *testing.T) {
	var m Memory
	if _, err := m.Append(Event{Type: TypeTurnStart, Turn: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Append(Event{Type: TypeTurnEnd, Turn: 1}); err != nil {
		t.Fatal(err)
	}
	if len(m.Events()) != 2 || m.Events()[1].Seq != 2 || m.Events()[0].Time.IsZero() {
		t.Fatalf("%+v", m.Events())
	}
}

func ptrMsg(m llm.Message) *llm.Message { return &m }

func TestEventLineJSONShape(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(dir, "/w", "p", "m")
	u := llm.TextMessage(llm.RoleUser, "hi")
	if _, err := s.Append(Event{Type: TypeUserMessage, Surface: true, Message: &u, Turn: 1}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	var last map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		last = map[string]any{}
		if err := json.Unmarshal([]byte(line), &last); err != nil {
			t.Fatal(err)
		}
	}
	if last["type"] != "event" || last["t"] != TypeUserMessage {
		t.Fatalf("shape = %v", last)
	}
	if _, err := time.Parse(time.RFC3339Nano, last["at"].(string)); err != nil && last["at"] == nil {
		t.Fatalf("missing at: %v", last)
	}
}

func TestMeasureAndQueryAndFeedback(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(dir, "/w", "p", "m")
	u := llm.TextMessage(llm.RoleUser, "count the widgets")
	a := llm.TextMessage(llm.RoleAssistant, "there are 12")
	_, _ = s.Append(Event{Type: TypeUserMessage, Surface: true, Message: &u})
	_, _ = s.Append(Event{Type: TypeAssistant, Surface: true, Message: &a})
	_, _ = s.Append(Event{Type: TypeToolCall, Name: "bash", Arguments: "ls", CallID: "c1"})
	_, _ = s.Append(Event{Type: TypeCompactSummary, Shadowed: []int{1}})
	m := Measure(s.Events(), func(msg llm.Message) int { return len(msg.Text()) })
	if m.LogRevision != 4 || len(m.Nodes) != 1 || m.Nodes[0].Seq != 2 {
		t.Fatalf("meter = %+v", m)
	}
	hits := QueryEvents(s.Events(), QueryFilter{Text: "widgets", Surface: SurfaceShadowed})
	if len(hits) != 1 || hits[0].Event.Seq != 1 {
		t.Fatalf("shadowed query = %+v", hits)
	}
	if err := s.Rate(0, RateUp, "good"); err != nil {
		t.Fatal(err)
	}
	fb, err := s.LoadFeedback()
	if err != nil || len(fb) != 1 || fb[0].Seq != 2 || fb[0].Rating != RateUp {
		t.Fatalf("feedback = %+v err=%v", fb, err)
	}
}

func TestUnknownTypeDoesNotBreakPath(t *testing.T) {
	// ensure we did not write a second jsonl beside the session
	dir := t.TempDir()
	s, _ := New(dir, "/w", "p", "m")
	_, _ = s.Append(Event{Type: TypeTurnStart, Turn: 1})
	ents, _ := os.ReadDir(dir)
	n := 0
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".jsonl") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("jsonl count = %d in %s", n, dir)
	}
	if filepath.Ext(s.Path()) != ".jsonl" {
		t.Fatalf("path = %s", s.Path())
	}
}

func TestQuerySnippetDoesNotSplitRunes(t *testing.T) {
	long := strings.Repeat("é", 200)
	events := []Event{{Seq: 1, Type: TypeToolResult, CallID: "c1", Name: "bash", Output: long}}
	hits := QueryEvents(events, QueryFilter{Tool: "bash"})
	if len(hits) != 1 {
		t.Fatalf("hits = %+v", hits)
	}
	if !utf8.ValidString(hits[0].Snippet) {
		t.Fatalf("snippet split a rune: %q", hits[0].Snippet)
	}
	if !strings.HasSuffix(hits[0].Snippet, "…") {
		t.Fatalf("snippet not truncated: %q", hits[0].Snippet)
	}
}

func TestConcurrentAppend(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(dir, "/w", "p", "m")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 5; j++ {
				if _, err := s.Append(Event{Type: TypeToolCall, Name: "bash"}); err != nil {
					t.Error(err)
					return
				}
				_ = s.Events()
			}
		}(i)
	}
	wg.Wait()
	re, _, err := Open(dir, s.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(re.Events()) != 40 {
		t.Fatalf("events on disk = %d, want 40", len(re.Events()))
	}
	seen := map[int]bool{}
	for _, e := range re.Events() {
		if seen[e.Seq] {
			t.Fatalf("duplicate seq %d", e.Seq)
		}
		seen[e.Seq] = true
	}
}
