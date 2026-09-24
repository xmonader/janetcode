package llm

import (
	"encoding/json"
	"strings"
)

// maxMissingClosers bounds how many forgotten closers one closer may stand
// in for. A model drops one or two; more is not a slip, and the bound keeps
// a hostile input from making the scan quadratic.
const maxMissingClosers = 4

// RepairJSON mends the structural slips models make in long tool-call
// arguments: a closing bracket or brace left out before an outer one
// (`{"rows":[[1],[2]}`), closers missing at the end, and a trailing comma
// before a closer. It never touches the inside of a string, and it gives
// up on an unterminated string: that is what a reply cut off mid-value
// looks like, and closing it would turn a half-written file into a valid
// call. ok is false unless the result parses.
func RepairJSON(s string) (string, bool) {
	out := make([]byte, 0, len(s)+8)
	var stack []byte
	inString, escaped := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inString {
			out = append(out, c)
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			stack = append(stack, '}')
		case '[':
			stack = append(stack, ']')
		case '}', ']':
			// A closer that matches something further down closes what
			// the model forgot on the way there; one that matches nothing
			// near the top is not a slip this can mend.
			at := -1
			for j := len(stack) - 1; j >= 0 && j >= len(stack)-maxMissingClosers; j-- {
				if stack[j] == c {
					at = j
					break
				}
			}
			if at < 0 {
				return "", false
			}
			out = trimTrailingComma(out)
			for j := len(stack) - 1; j > at; j-- {
				out = append(out, stack[j])
			}
			stack = stack[:at]
		}
		out = append(out, c)
	}
	if inString {
		return "", false
	}
	if len(stack) > 0 {
		out = trimTrailingComma(out)
		for j := len(stack) - 1; j >= 0; j-- {
			out = append(out, stack[j])
		}
	}
	if !json.Valid(out) {
		return "", false
	}
	return string(out), true
}

// trimTrailingComma drops a comma (and the space after it) at the end of
// what has been written, so `[1,2,]` closes as `[1,2]`.
func trimTrailingComma(b []byte) []byte {
	i := len(b)
	for i > 0 && strings.IndexByte(" \t\r\n", b[i-1]) >= 0 {
		i--
	}
	if i > 0 && b[i-1] == ',' {
		return b[:i-1]
	}
	return b
}

// invalidArgumentsKey marks a tool call whose arguments were not JSON and
// could not be repaired. The call is kept, with the raw text under this
// key, so the history stays valid for every provider and the model is told
// what was wrong instead of the turn failing.
const invalidArgumentsKey = "_janetcode_invalid_json"

// InvalidArguments wraps arguments that are not JSON into an object that is.
func InvalidArguments(raw string) json.RawMessage {
	b, _ := json.Marshal(map[string]string{invalidArgumentsKey: raw})
	return b
}

// UnparsedArguments reports whether input is the wrapper InvalidArguments
// made, and returns the model's original text.
func UnparsedArguments(input json.RawMessage) (string, bool) {
	if !strings.Contains(string(input), invalidArgumentsKey) {
		return "", false
	}
	var m map[string]string
	if json.Unmarshal(input, &m) != nil || len(m) != 1 {
		return "", false
	}
	raw, ok := m[invalidArgumentsKey]
	return raw, ok
}
