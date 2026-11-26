package tool

import "encoding/json"

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
