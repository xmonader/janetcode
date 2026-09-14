package llm

import (
	"encoding/json"
	"errors"
	"strings"
)

// ErrNoJSONObject is returned by ExtractJSONObject when text has no "{ … }"
// span to decode.
var ErrNoJSONObject = errors.New("no JSON object in the model's reply")

// ExtractJSONObject decodes the JSON object in a model's reply into v,
// tolerating a reply wrapped in a code fence or surrounded by prose: it
// takes the span from the first "{" to the last "}" and unmarshals that.
// A model asked for "only JSON" still often adds a sentence or a fence,
// and nothing is lost by reading past them.
func ExtractJSONObject(text string, v any) error {
	start, end := strings.Index(text, "{"), strings.LastIndex(text, "}")
	if start < 0 || end <= start {
		return ErrNoJSONObject
	}
	return json.Unmarshal([]byte(text[start:end+1]), v)
}
