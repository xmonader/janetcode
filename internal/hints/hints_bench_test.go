package hints

import "testing"

// The hint engine is asked a question on every turn boundary, every
// denial and every paste. Evaluating is a walk over the rules and must
// stay a walk: no allocation, no I/O. BenchmarkEvalNoHint measures the
// common case, where nothing fires.
func BenchmarkEvalNoHint(b *testing.B) {
	e := New(nil, nil)
	e.Record = func(string) {}
	s := &State{Used: func(string) bool { return true }}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = e.Eval(s)
	}
}

// TestEvalDoesNotAllocate is the guard behind the benchmark: a rule
// added later that formats a string, reads a file or walks a map on
// every evaluation would show up here rather than in a profile.
func TestEvalDoesNotAllocate(t *testing.T) {
	e := New(nil, nil)
	e.Record = func(string) {}
	// Every rule spent, so the walk runs to the end without firing.
	for _, r := range e.Rules {
		e.fire(r.ID, true)
	}
	s := &State{Used: func(string) bool { return false }}
	if got := testing.AllocsPerRun(100, func() { _ = e.Eval(s) }); got != 0 {
		t.Fatalf("Eval allocates %.1f times per call", got)
	}
}
