package config

import "testing"

func TestBlindTesterModelResolvesASlot(t *testing.T) {
	c := &Config{Model: "main-m", RouteCheap: "cheap-m", StrongModel: "strong-m"}
	for v, want := range map[string]string{
		"on": "cheap-m", "cheap": "cheap-m", " Strong ": "strong-m", "main": "main-m", "model": "main-m",
		// An unset slot is the session's own model.
		"advisor": "main-m",
	} {
		c.BlindTester = v
		if got, on := c.BlindTesterModel(); !on || got != want {
			t.Errorf("%q: %q %v, want %q on", v, got, on, want)
		}
	}
	for _, v := range []string{"", "off", "gpt-5", "cheapest"} {
		c.BlindTester = v
		if got, on := c.BlindTesterModel(); on {
			t.Errorf("%q is on (%q); want off", v, got)
		}
	}
}

func TestSetBlindTester(t *testing.T) {
	c := &Config{}
	if _, err := blindTesterKey(c, "strong"); err != nil || c.BlindTester != "strong" {
		t.Fatalf("strong: %v %q", err, c.BlindTester)
	}
	if v, err := blindTesterKey(c, "off"); err != nil || v != "off" || c.BlindTester != "" {
		t.Fatalf("off: %v %v %q", v, err, c.BlindTester)
	}
	if _, err := blindTesterKey(c, "gpt-5"); err == nil {
		t.Fatal("a model name was accepted where a slot is wanted")
	}
	if setters["blind_tester"] == nil {
		t.Fatal("blind_tester is not settable")
	}
}
