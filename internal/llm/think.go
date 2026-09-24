package llm

import "strings"

// thinkOpeners and thinkClosers are the tags open-weight models put their
// reasoning between when the server has no reasoning parser: Qwen3,
// DeepSeek-R1 distills and MiniMax on Ollama, vLLM or llama.cpp.
var (
	thinkOpeners = []string{"<think>", "<thinking>"}
	thinkClosers = []string{"</think>", "</thinking>"}
)

// ThinkSplitter separates reasoning a model wrote inline, between <think>
// tags, from the reply it streams. Without it the reasoning is shown as the
// answer, kept in the history as text and resent every turn.
//
// Only a tag that opens the reply counts: a reply that mentions <think>
// halfway through, answering a question about the tag, is left alone. A
// tag split across chunks is held back until it is known to be one.
type ThinkSplitter struct {
	pending  string
	inThink  bool
	decided  bool // the reply has visible text, or its reasoning ended
	trimLead bool // drop the whitespace that separates reasoning from reply
}

// Feed takes the next piece of content and returns what of it is reply and
// what is reasoning.
func (t *ThinkSplitter) Feed(chunk string) (text, think string) {
	buf := t.pending + chunk
	t.pending = ""
	var tb, kb strings.Builder
	for buf != "" {
		switch {
		case t.inThink:
			i, n := indexFold(buf, thinkClosers)
			if i < 0 {
				keep := partialSuffix(buf, thinkClosers)
				kb.WriteString(buf[:len(buf)-keep])
				t.pending = buf[len(buf)-keep:]
				return tb.String(), kb.String()
			}
			kb.WriteString(buf[:i])
			buf = buf[i+n:]
			t.inThink, t.decided, t.trimLead = false, true, true
		case !t.decided:
			rest := strings.TrimLeft(buf, " \t\r\n")
			if rest == "" {
				t.pending = buf
				return tb.String(), kb.String()
			}
			if n := prefixFold(rest, thinkOpeners); n > 0 {
				buf = rest[n:]
				t.inThink = true
				continue
			}
			if partialPrefix(rest, thinkOpeners) {
				t.pending = buf
				return tb.String(), kb.String()
			}
			t.decided = true
		default:
			if t.trimLead {
				buf = strings.TrimLeft(buf, " \t\r\n")
				if buf == "" {
					return tb.String(), kb.String()
				}
				t.trimLead = false
			}
			tb.WriteString(buf)
			buf = ""
		}
	}
	return tb.String(), kb.String()
}

// Flush returns what was held back when the stream ends: reasoning if it
// never closed, reply text otherwise.
func (t *ThinkSplitter) Flush() (text, think string) {
	rest := t.pending
	t.pending = ""
	if t.inThink {
		return "", rest
	}
	if t.trimLead {
		rest = strings.TrimLeft(rest, " \t\r\n")
	}
	return rest, ""
}

// indexFold finds the first of tags in s, ignoring case, and returns its
// offset and length, or -1. It does not lower-case s: that can change the
// byte length of non-ASCII text and put the offsets off.
func indexFold(s string, tags []string) (int, int) {
	for i := strings.IndexByte(s, '<'); i >= 0; {
		if n := prefixFold(s[i:], tags); n > 0 {
			return i, n
		}
		j := strings.IndexByte(s[i+1:], '<')
		if j < 0 {
			break
		}
		i += j + 1
	}
	return -1, 0
}

// prefixFold returns the length of the tag s starts with, ignoring case.
func prefixFold(s string, tags []string) int {
	for _, tag := range tags {
		if len(s) >= len(tag) && strings.EqualFold(s[:len(tag)], tag) {
			return len(tag)
		}
	}
	return 0
}

// partialPrefix reports whether s is the start of one of tags, cut short.
func partialPrefix(s string, tags []string) bool {
	for _, tag := range tags {
		if len(s) < len(tag) && strings.EqualFold(s, tag[:len(s)]) {
			return true
		}
	}
	return false
}

// partialSuffix returns how many bytes at the end of s could be the start
// of one of tags, so they are held until the next chunk says.
func partialSuffix(s string, tags []string) int {
	best := 0
	for _, tag := range tags {
		for n := min(len(tag)-1, len(s)); n > best; n-- {
			if strings.EqualFold(s[len(s)-n:], tag[:n]) {
				best = n
				break
			}
		}
	}
	return best
}
