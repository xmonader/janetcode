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

// A wide rune fills two cells, so a carriage return that overlays it
// replaces cells, not runes.
func TestDisplayWideRunesOverstrike(t *testing.T) {
	for in, want := range map[string]string{
		"你好\rab":          "ab好", // two narrow cells cover one wide rune
		"你好\ra":           "a 好", // half a wide rune goes, not the next one
		"ab\r你":           "你",
		"abc\r你":          "你c",
		"a你\rXY":          "XY ",
		"你好\r你":           "你好",
		"\U0001F600x\rab": "abx",
	} {
		if got := Display(in); got != want {
			t.Errorf("Display(%q) = %q, want %q", in, got, want)
		}
	}
}
