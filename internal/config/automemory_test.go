package config

import "testing"

func TestAutoMemoryHeadlessNeedsExplicitOn(t *testing.T) {
	on, off := true, false
	for _, tc := range []struct {
		mem                 *bool
		interactive, headed bool
	}{
		{nil, true, false},
		{&on, true, true},
		{&off, false, false},
	} {
		c := &Config{Memory: tc.mem}
		if got := c.AutoMemory(); got != tc.interactive {
			t.Errorf("Memory=%v: AutoMemory=%v, want %v", tc.mem, got, tc.interactive)
		}
		if got := c.AutoMemoryHeadless(); got != tc.headed {
			t.Errorf("Memory=%v: AutoMemoryHeadless=%v, want %v", tc.mem, got, tc.headed)
		}
	}
}
