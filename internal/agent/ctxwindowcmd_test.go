package agent

import "testing"

func TestParseWindow(t *testing.T) {
	for _, c := range []struct {
		in   string
		want int
		bad  bool
	}{
		{"200k", 200_000, false}, {"1m", 1_000_000, false}, {"1.5M", 1_500_000, false},
		{"500000", 500_000, false}, {"default", 0, false}, {"", 0, true},
		{"abc", 0, true}, {"1k", 0, true}, {"99m", 0, true}, {"-5k", 0, true},
	} {
		got, err := ParseWindow(c.in)
		if (err != nil) != c.bad || got != c.want {
			t.Errorf("ParseWindow(%q)=%d,%v", c.in, got, err)
		}
	}
}

func TestCompactThresholdUsesOverride(t *testing.T) {
	a := &Agent{}
	a.ContextWindow = 100_000
	if got := a.CompactThreshold(); got != 75_000 {
		t.Fatalf("model window: %d", got)
	}
	res, _ := a.ContextWindowCommand("1m")
	if res.Error != "" || a.CompactThreshold() != 750_000 {
		t.Fatalf("override: %d %q", a.CompactThreshold(), res.Error)
	}
	if res, _ = a.ContextWindowCommand("nope"); res.Error == "" {
		t.Fatal("bad size accepted")
	}
	a.ContextWindowCommand("default")
	if a.CompactThreshold() != 75_000 {
		t.Fatal("reset failed")
	}
}
