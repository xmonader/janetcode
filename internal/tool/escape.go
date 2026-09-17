package tool

import "strings"

// The harness wraps some of what it sends the model in tags of its own:
// <harness> around text its own tooling wrote, <system-reminder> around a
// note the model must not mistake for the user, <assistant> around a
// seeded turn. A tool result is data, and data that contains one of those
// tags — a file that documents them, a log that quotes one, a page fetched
// off the web that was written to — would otherwise read as the harness
// speaking.
//
// delimiters is the closed list of tags that carry that authority.
var delimiters = map[string]bool{
	"system-reminder": true, "harness": true, "assistant": true,
	"system": true, "user": true, "stdin": true, "attached": true,
	"skill": true, "resource": true,
}

// EscapeDelimiters neutralises harness delimiters in untrusted text by
// escaping the opening angle bracket. The text still reads the same; it
// no longer parses as a delimiter. Nothing else in the text is touched,
// so it stays something the model can quote back.
//
// The consumer is a language model, not a strict parser, so the match is
// deliberately loose: it was an exact-lowercase, exact-form comparison
// against "<tag>", "</tag>" and "<tag ", which let <SYSTEM-REMINDER>,
// <System-Reminder>, <system-reminder/> and a tag broken by a newline or
// a tab through untouched — all of which a model reads as the tag.
func EscapeDelimiters(s string) string {
	if !strings.Contains(s, "<") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 16)
	for i := 0; i < len(s); {
		if s[i] != '<' {
			b.WriteByte(s[i])
			i++
			continue
		}
		if n := delimiterAt(s[i:]); n > 0 {
			b.WriteString("&lt;")
			b.WriteString(s[i+1 : i+n])
			i += n
			continue
		}
		b.WriteByte('<')
		i++
	}
	return b.String()
}

// delimiterAt reports the length of the tag opening at the start of s, or
// zero when there is none. s begins with '<'.
func delimiterAt(s string) int {
	i := 1
	if i < len(s) && s[i] == '/' { // a closing tag
		i++
	}
	start := i
	for i < len(s) && isTagByte(s[i]) {
		i++
	}
	if i == start {
		return 0
	}
	if !delimiters[strings.ToLower(s[start:i])] {
		return 0
	}
	if i >= len(s) {
		// The text ends inside the tag. Escaping it is still the safe
		// reading: what follows could complete it.
		return i
	}
	// What follows the name decides whether a model reads this as a tag:
	// the close, a self-close, or whitespace before attributes. Anything
	// else means a longer name that merely starts the same way, such as
	// <username> against <user>.
	switch c := s[i]; c {
	case '>', '/', ' ', '\t', '\n', '\r', '\f', '\v':
		return i + 1
	}
	return 0
}

func isTagByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_'
}
