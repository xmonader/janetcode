package llm

import "testing"

// split feeds chunks through a splitter and returns everything it produced.
func split(chunks ...string) (text, think string) {
	var s ThinkSplitter
	for _, c := range chunks {
		a, b := s.Feed(c)
		text += a
		think += b
	}
	a, b := s.Flush()
	return text + a, think + b
}

func TestThinkSplitter(t *testing.T) {
	cases := []struct {
		name        string
		chunks      []string
		text, think string
	}{
		{"whole", []string{"<think>plan it</think>\n\nDone."}, "Done.", "plan it"},
		{"tags split across chunks", []string{"<th", "ink>pl", "an</thi", "nk>", "\n", "Done."}, "Done.", "plan"},
		{"case and long form", []string{"  <Thinking>x</THINKING>ok"}, "ok", "x"},
		{"no tags", []string{"Hello ", "world"}, "Hello world", ""},
		{"tag mid reply is text", []string{"Use a <think> tag."}, "Use a <think> tag.", ""},
		{"never closed", []string{"<think>still going"}, "", "still going"},
		{"lone less-than", []string{"<", "b>bold</b>"}, "<b>bold</b>", ""},
		{"non-ASCII before the closer", []string{"<think>İİ</think>ok"}, "ok", "İİ"},
		{"only one reasoning block", []string{"<think>a</think>b<think>c</think>"}, "b<think>c</think>", "a"},
	}
	for _, c := range cases {
		text, think := split(c.chunks...)
		if text != c.text || think != c.think {
			t.Errorf("%s: text %q think %q; want %q %q", c.name, text, think, c.text, c.think)
		}
	}
}
