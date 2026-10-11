package agent

import (
	"testing"
	"time"
)

func TestLoopArg(t *testing.T) {
	for _, c := range []struct {
		in     string
		every  time.Duration
		prompt string
		bad    bool
	}{
		{"ping", 5 * time.Minute, "ping", false},
		{"10m check ci", 10 * time.Minute, "check ci", false},
		{"10m", 0, "", true},
		{"25h ping", 0, "", true},
		{"", 0, "", true},
	} {
		every, prompt, err := LoopArg(c.in)
		if (err != nil) != c.bad || every != c.every || prompt != c.prompt {
			t.Errorf("LoopArg(%q) = %v, %q, %v", c.in, every, prompt, err)
		}
	}
}
