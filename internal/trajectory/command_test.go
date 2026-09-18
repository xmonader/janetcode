package trajectory

import (
	"strings"
	"testing"
	"time"
)

func TestCommandDispatch(t *testing.T) {
	ev := sample(time.Now())
	for _, tc := range []struct {
		args []string
		want string
		ok   bool
	}{
		{nil, "turn 1", true},
		{[]string{"all"}, "run the tests", true},
		{[]string{"turns"}, "2 steps", true},
		{[]string{"turn", "1"}, "Step 1", true},
		{[]string{"turn", "9"}, "no turn 9", true},
		{[]string{"turn"}, "usage:", false},
		{[]string{"turn", "x"}, "takes a turn number", false},
		{[]string{"slow"}, "slowest records", true},
		{[]string{"slow", "1"}, "slowest records", true},
		{[]string{"slow", "x"}, "takes a count", false},
		{[]string{"grep", "gofmt"}, "1 record matching", true},
		{[]string{"grep"}, "usage:", false},
		{[]string{"2"}, "#2", true},
		{[]string{"999"}, "no record #999", true},
		{[]string{"help"}, "/trajectory slow", true},
		{[]string{"nonsense"}, "unknown:", false},
	} {
		got, ok := Command(ev, tc.args, 100)
		if ok != tc.ok {
			t.Fatalf("%v: ok = %v, want %v (%s)", tc.args, ok, tc.ok, got)
		}
		if !strings.Contains(got, tc.want) {
			t.Fatalf("%v: want %q in:\n%s", tc.args, tc.want, got)
		}
	}
}

// The default view is the tail: an hour-long session has hundreds of
// records and a screenful of the recent ones is what it is opened for.
func TestCommandDefaultsToTheTail(t *testing.T) {
	ev := sample(time.Now())
	for i := 0; i < tail*2; i++ {
		ev = append(ev, ev[4], ev[5]) // a great many bash calls
	}
	out, ok := Command(ev, nil, 100)
	if !ok {
		t.Fatal("the default view failed")
	}
	if !strings.Contains(out, "before this") {
		t.Fatalf("the tail did not say what it dropped:\n%s", out[:200])
	}
	if n := strings.Count(out, "\n"); n > tail+20 {
		t.Fatalf("the default view is %d lines", n)
	}
	if all, _ := Command(ev, []string{"all"}, 100); strings.Count(all, "\n") <= strings.Count(out, "\n") {
		t.Fatal("all is not longer than the tail")
	}
}
