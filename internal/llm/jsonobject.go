package llm

import (
	"encoding/json"
	"errors"
	"strings"
)

// ErrNoJSONObject is returned by ExtractJSONObject when text has no "{ … }"
// span to decode.
var ErrNoJSONObject = errors.New("no JSON object in the model's reply")

// maxJSONCandidates bounds the second pass. A reply is a few kilobytes of
// model output, but the scan is quadratic in the number of braces in it,
// and a hostile one is all braces.
const maxJSONCandidates = 32

// ExtractJSONObject decodes the JSON object in a model's reply into v,
// tolerating a reply wrapped in a code fence or surrounded by prose: it
// takes the span from the first "{" to the last "}" and unmarshals that.
// A model asked for "only JSON" still often adds a sentence or a fence,
// and nothing is lost by reading past them.
//
// When that span does not decode — the prose around the answer had
// braces of its own, or the model wrote a second object after the one it
// was asked for — each "{" in turn is tried as the start of a balanced
// object, and the first that decodes wins. The whole-span attempt comes
// first because it is what handles a reply whose object is followed by a
// stray closing brace, and its error is what a total failure reports.
func ExtractJSONObject(text string, v any) error {
	start, end := strings.Index(text, "{"), strings.LastIndex(text, "}")
	if start < 0 || end <= start {
		return ErrNoJSONObject
	}
	err := json.Unmarshal([]byte(text[start:end+1]), v)
	if err == nil {
		return nil
	}
	for i, tried := start, 0; i >= 0 && tried < maxJSONCandidates; tried++ {
		if j := objectEnd(text, i); j > i {
			if json.Unmarshal([]byte(text[i:j]), v) == nil {
				return nil
			}
		}
		next := strings.IndexByte(text[i+1:], '{')
		if next < 0 {
			break
		}
		i += next + 1
	}
	return err
}

// objectEnd returns the offset just past the object that opens at start,
// or -1 when the braces never balance. Braces inside strings are text,
// and a backslash escapes the next byte of a string.
func objectEnd(text string, start int) int {
	depth, inString, escaped := 0, false, false
	for i := start; i < len(text); i++ {
		c := text[i]
		switch {
		case escaped:
			escaped = false
		case inString && c == '\\':
			escaped = true
		case c == '"':
			inString = !inString
		case inString:
		case c == '{':
			depth++
		case c == '}':
			if depth--; depth == 0 {
				return i + 1
			}
		}
	}
	return -1
}
