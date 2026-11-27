package tool

import (
	"encoding/json"
	"strings"
)

// argAliases are argument names models were trained on that a tool's schema
// spells differently. gpt-oss sends bash a "cmd", and providers that check
// arguments against the schema (Groq) refuse the call outright, so the
// schema declares the alias and NormalizeArgs renames it before anything —
// permissions, guards, the tool itself — reads the input.
var argAliases = map[string]map[string]string{
	"bash": {"cmd": "command"},
}

// NormalizeArgs renames a tool's aliased arguments to their schema names.
// An alias is dropped when the real name is already set. Input that is not
// a JSON object, or has no alias, is returned unchanged.
func NormalizeArgs(name string, input json.RawMessage) json.RawMessage {
	aliases := argAliases[name]
	if len(aliases) == 0 {
		return input
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(input, &m) != nil {
		return input
	}
	changed := false
	if name == "bash" {
		for _, k := range []string{"command", "cmd"} {
			if s, ok := argvCommand(m[k]); ok {
				m[k], _ = json.Marshal(s)
				changed = true
			}
		}
	}
	for alias, real := range aliases {
		v, ok := m[alias]
		if !ok {
			continue
		}
		if _, set := m[real]; !set {
			m[real] = v
		}
		delete(m, alias)
		changed = true
	}
	if !changed {
		return input
	}
	out, err := json.Marshal(m)
	if err != nil {
		return input
	}
	return out
}

// argvCommand turns a command sent as an argv list into the string the
// schema asks for. gpt-oss was trained on a shell tool that takes
// ["bash", "-lc", "<script>"], and sends that shape to bash: the script is
// the third element. Any other list is joined with each word quoted.
func argvCommand(raw json.RawMessage) (string, bool) {
	var argv []string
	if len(raw) == 0 || raw[0] != '[' || json.Unmarshal(raw, &argv) != nil || len(argv) == 0 {
		return "", false
	}
	if len(argv) == 3 && (argv[0] == "bash" || argv[0] == "sh" || argv[0] == "zsh") && (argv[1] == "-c" || argv[1] == "-lc") {
		return argv[2], true
	}
	words := make([]string, len(argv))
	for i, w := range argv {
		if w != "" && strings.IndexFunc(w, func(r rune) bool {
			return !(r == '-' || r == '_' || r == '.' || r == '/' || r == '=' || r == ':' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z')
		}) < 0 {
			words[i] = w
		} else {
			words[i] = "'" + strings.ReplaceAll(w, "'", `'\''`) + "'"
		}
	}
	return strings.Join(words, " "), true
}
