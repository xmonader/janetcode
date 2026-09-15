package agent

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

// effortProvider is a provider that takes a reasoning tier, so /effort
// can report that the tier is actually sent.
type effortProvider struct{ fake }

func (e *effortProvider) SupportsEffort() bool { return true }

// The tier must reach the provider on the next request after it changes,
// not at the next process start: the provider used to be built once with
// the configured tier, so a mid-session change did nothing.
func TestEffortRidesOnTheNextRequest(t *testing.T) {
	f := &effortProvider{fake: fake{responses: []*llm.Response{text("one"), text("two"), text("three")}}}
	a, _ := newAgent(t, &f.fake, nil)
	a.Provider = f
	a.Effort = "medium"
	ctx := context.Background()
	if err := a.Run(ctx, "first", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := a.EffortCommand("max"); err != nil {
		t.Fatal(err)
	}
	if err := a.Run(ctx, "second", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := a.EffortCommand("down"); err != nil {
		t.Fatal(err)
	}
	if err := a.Run(ctx, "third", nil); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range f.fake.requests {
		got = append(got, r.Effort)
	}
	want := []string{"medium", "max", "xhigh"}
	if len(got) != len(want) {
		t.Fatalf("efforts sent = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("efforts sent = %v, want %v", got, want)
		}
	}
}

func TestEffortCommandParsing(t *testing.T) {
	cases := []struct {
		start, arg, want, note string
	}{
		{"medium", "high", "high", "effort high"},
		{"medium", "HIGH", "high", "effort high"},
		{"medium", " xhigh ", "xhigh", "effort xhigh"},
		{"medium", "up", "high", "effort high"},
		{"medium", "down", "low", "effort low"},
		{"max", "up", "max", "already at max"},
		{"low", "down", "", "effort default"},
		{"", "down", "", "already at default"},
		{"", "up", "low", "effort low"},
		{"high", "default", "", "effort default"},
		{"high", "ultra", "high", ""},
	}
	for _, c := range cases {
		a := &Agent{Effort: c.start}
		res, err := a.EffortCommand(c.arg)
		if err != nil {
			t.Fatalf("/effort %q: %v", c.arg, err)
		}
		if got := a.Settings().Effort; got != c.want {
			t.Errorf("/effort %q from %q left effort %q, want %q", c.arg, c.start, got, c.want)
		}
		if c.note == "" { // a bad tier is reported, not applied
			if res.Error == "" || !strings.Contains(res.Error, "ultra") {
				t.Errorf("/effort %q: error = %q, want the tier named", c.arg, res.Error)
			}
			continue
		}
		if res.Error != "" {
			t.Errorf("/effort %q: unexpected error %q", c.arg, res.Error)
		}
		if len(res.Notes) != 1 || !strings.Contains(res.Notes[0], c.note) {
			t.Errorf("/effort %q: notes = %v, want one mentioning %q", c.arg, res.Notes, c.note)
		}
	}
}

// /effort alone reports the tier, the ladder and what the provider does
// with it — including the answer "nothing" for a provider without one.
func TestEffortReport(t *testing.T) {
	honours := &effortProvider{}
	a := &Agent{Provider: honours, Effort: "xhigh"}
	res, err := a.EffortCommand("")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Sheet, "effort: xhigh") || !strings.Contains(res.Sheet, "[xhigh]") {
		t.Fatalf("sheet = %q", res.Sheet)
	}
	if !strings.Contains(res.Sheet, "asks for the tier") {
		t.Fatalf("a provider that takes a tier must say so: %q", res.Sheet)
	}
	plain := &Agent{Provider: &fake{}, Effort: "max"}
	res, err = plain.EffortCommand("")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Sheet, "does not take a reasoning tier") {
		t.Fatalf("a provider without a tier must say so: %q", res.Sheet)
	}
	// Setting it on such a provider still works, with the caveat.
	set, err := plain.EffortCommand("low")
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Tips) != 1 || !strings.Contains(set.Tips[0], "not sent") {
		t.Fatalf("tips = %v", set.Tips)
	}
}

// Settings is the one safe way to read the tier while a turn runs.
func TestEffortSettingsRoundTrip(t *testing.T) {
	a := &Agent{Effort: "low"}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				a.UpdateSettings(func(s *Settings) { s.Effort = "high" })
				_ = a.Settings().Effort
			}
		}()
	}
	wg.Wait()
	if got := a.Settings().Effort; got != "high" {
		t.Fatalf("effort = %q", got)
	}
}
