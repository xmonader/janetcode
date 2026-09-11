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
var delimiters = []string{
	"system-reminder", "harness", "assistant", "system", "user",
	"stdin", "attached", "skill", "resource",
}

// EscapeDelimiters neutralises harness delimiters in untrusted text by
// escaping the opening angle bracket. The text still reads the same; it
// no longer parses as a delimiter. Nothing else in the text is touched,
// so it stays something the model can quote back.
func EscapeDelimiters(s string) string {
	if !strings.Contains(s, "<") {
		return s
	}
	for _, tag := range delimiters {
		for _, form := range []string{"<" + tag + ">", "</" + tag + ">", "<" + tag + " "} {
			if strings.Contains(s, form) {
				s = strings.ReplaceAll(s, form, "&lt;"+form[1:])
			}
		}
	}
	return s
}
