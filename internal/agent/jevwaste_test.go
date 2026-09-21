package agent

import (
	"testing"

	"github.com/xmonader/janetcode/internal/session"
)

func wasteCall(seq int, id, name, args string) session.Event {
	return session.Event{Seq: seq, Type: session.TypeToolCall, CallID: id, Name: name, Arguments: args}
}

func wasteResult(seq int, id string, isErr, denied bool) session.Event {
	return session.Event{Seq: seq, Type: session.TypeToolResult, CallID: id, IsError: isErr, Denied: denied, Output: "out"}
}

// TestWasteLabelsErroredAndDenied: the two shapes rule (a) covers.
func TestWasteLabelsErroredAndDenied(t *testing.T) {
	ev := []session.Event{
		wasteCall(1, "a", "read_file", `{"path":"a.go"}`), wasteResult(2, "a", true, false),
		wasteCall(3, "b", "bash", `{"command":"rm -rf /"}`), wasteResult(4, "b", false, true),
		wasteCall(5, "c", "read_file", `{"path":"c.go"}`), wasteResult(6, "c", false, false),
	}
	got := WasteLabels(ev, true)
	want := []string{WasteErrored, WasteErrored, WasteNone}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("call %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestWasteLabelsLoops: an identical run of three, and three reads of one
// path inside a window of five that are not adjacent.
func TestWasteLabelsLoops(t *testing.T) {
	var ev []session.Event
	for i := 0; i < 3; i++ {
		ev = append(ev, wasteCall(1+2*i, string(rune('a'+i)), "grep", `{"pattern":"Foo"}`))
		ev = append(ev, wasteResult(2+2*i, string(rune('a'+i)), false, false))
	}
	if got := WasteLabels(ev, true); got[0] != WasteLoop || got[1] != WasteLoop || got[2] != WasteLoop {
		t.Errorf("identical run not labelled a loop: %v", got)
	}

	var ev2 []session.Event
	names := []string{"x.go", "other.go", "x.go", "another.go", "x.go"}
	for i, n := range names {
		id := string(rune('a' + i))
		ev2 = append(ev2, wasteCall(1+2*i, id, "read_file", `{"path":"`+n+`"}`))
		ev2 = append(ev2, wasteResult(2+2*i, id, false, false))
	}
	got := WasteLabels(ev2, true)
	for _, i := range []int{0, 2, 4} {
		if got[i] != WasteLoop {
			t.Errorf("same-path window: call %d = %q, want loop", i, got[i])
		}
	}
	for _, i := range []int{1, 3} {
		if got[i] != WasteNone {
			t.Errorf("same-path window: call %d = %q, want none", i, got[i])
		}
	}
}

// TestWasteLabelsDeadTail: only a failing run has one, it starts after the
// last successful edit, and a green gate after that edit moves the line.
func TestWasteLabelsDeadTail(t *testing.T) {
	ev := []session.Event{
		wasteCall(1, "a", "edit_file", `{"path":"a.go"}`), wasteResult(2, "a", false, false),
		wasteCall(3, "b", "read_file", `{"path":"b.go"}`), wasteResult(4, "b", false, false),
		wasteCall(5, "c", "read_file", `{"path":"c.go"}`), wasteResult(6, "c", false, false),
	}
	if got := WasteLabels(ev, true); got[1] != WasteNone || got[2] != WasteNone {
		t.Errorf("a passing run has no dead tail: %v", got)
	}
	got := WasteLabels(ev, false)
	if got[0] != WasteNone || got[1] != WasteDeadTail || got[2] != WasteDeadTail {
		t.Errorf("dead tail: %v", got)
	}

	// A green gate at seq 4 puts the two later reads on the right side of the
	// line, so nothing after it is dead.
	withGate := append([]session.Event{}, ev[:4]...)
	withGate = append(withGate, session.Event{Seq: 7, Type: session.TypeGate})
	withGate = append(withGate, ev[4:]...)
	if got := WasteLabels(withGate, false); got[2] != WasteNone {
		t.Errorf("a call after a green gate is not a dead tail: %v", got)
	}
}
