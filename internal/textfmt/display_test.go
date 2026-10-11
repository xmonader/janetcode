package textfmt

import "testing"

func TestDisplay(t *testing.T) {
	for in, want := range map[string]string{
		"AAAA 10%\rBB":     "BBAA 10%",
		"done\a":           "done",
		"abc\bX":           "abX",
		"line\r\nnext":     "line\nnext",
		"plain\ttext\n":    "plain\ttext\n",
		"x\x1b]52;c;AA\a!": "x!", // a clipboard write is an escape, not text
		"x\u009b2Jy":       "xy", // a C1 CSI and its parameters
	} {
		if got := Display(in); got != want {
			t.Errorf("Display(%q) = %q, want %q", in, got, want)
		}
	}
}
