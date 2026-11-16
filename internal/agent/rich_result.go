package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"runtime/debug"
	"strings"

	"github.com/xmonader/janetcode/internal/egress"
	"github.com/xmonader/janetcode/internal/secret"
	"github.com/xmonader/janetcode/internal/tool"
)

// runSafelyResult preserves machine-readable data through the same panic and
// credential boundary as text. Structured values never bypass scrubbing.
func (a *Agent) runSafelyResult(ctx context.Context, t tool.Tool, input json.RawMessage) (res tool.Result, err error) {
	res, err = runSafelyResult(ctx, t, input)
	if a.Off["scrub"] {
		return res, err
	}
	var n, m int
	res.Text, n = secret.Redact(res.Text)
	res.Data, m = redactToolData(res.Data)
	n += m
	if err != nil {
		if red, count := secret.Redact(err.Error()); count > 0 {
			err = errors.New(red)
			n += count
		}
	}
	if n > 0 {
		egress.From(ctx).Add(egress.Entry{Kind: egress.KindRedacted, What: "in tool output", Count: n})
	}
	return res, err
}

func runSafelyResult(ctx context.Context, t tool.Tool, input json.RawMessage) (res tool.Result, err error) {
	defer func() {
		if r := recover(); r != nil {
			res = tool.Result{}
			err = fmt.Errorf("tool %s panicked: %v\n%s", t.Name(), r, debug.Stack())
		}
	}()
	if rich, ok := t.(tool.Rich); ok {
		return rich.RunRich(ctx, input)
	}
	res.Text, err = t.Run(ctx, input)
	return res, err
}

func redactToolData(data json.RawMessage) (json.RawMessage, int) {
	if len(data) == 0 {
		return nil, 0
	}
	var value any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if !json.Valid(data) || dec.Decode(&value) != nil {
		return nil, 0
	}
	// A credential can span object fields (grep's per-line matches). Check
	// before leaf scrubbing erases its BEGIN marker. Cross-field redaction
	// may produce valid JSON while silently deleting objects or properties.
	normalized, err := json.Marshal(value)
	if err != nil {
		return nil, 0
	}
	if red, n := secret.Redact(string(normalized)); n > 0 {
		var redValue any
		redDecoder := json.NewDecoder(strings.NewReader(red))
		redDecoder.UseNumber()
		if !json.Valid([]byte(red)) || redDecoder.Decode(&redValue) != nil || !sameToolDataShape(value, redValue) {
			return nil, n
		}
	}
	count := 0
	var scrub func(any) any
	scrub = func(v any) any {
		switch x := v.(type) {
		case string:
			red, n := secret.Redact(x)
			count += n
			return red
		case []any:
			// Source windows are arrays of lines. Scrubbing each line first
			// would erase a PEM's BEGIN marker while leaving its body intact.
			// When a cross-line credential collapses line breaks, hide the
			// entire window while preserving its size and source positions.
			lines := make([]string, len(x))
			allStrings := len(x) > 0
			for i, item := range x {
				var ok bool
				lines[i], ok = item.(string)
				if !ok {
					allStrings = false
					break
				}
			}
			if allStrings {
				joined := strings.Join(lines, "\n")
				red, n := secret.Redact(joined)
				if n > 0 && strings.Count(red, "\n") < strings.Count(joined, "\n") {
					for i := range x {
						x[i] = secret.Placeholder
					}
					count += n
					return x
				}
			}
			for i := range x {
				x[i] = scrub(x[i])
			}
		case map[string]any:
			for k, v := range x {
				x[k] = scrub(v)
			}
		}
		return v
	}
	encoded, err := json.Marshal(scrub(value))
	if err != nil {
		return nil, count
	}
	// Also catch contextual credentials such as {"password":"opaque-value"}.
	red, n := secret.Redact(string(encoded))
	count += n
	if !json.Valid([]byte(red)) {
		return nil, count
	}
	return json.RawMessage(red), count
}

// String arrays may shrink in the preflight check: scrub preserves their
// positions by conservatively hiding the whole source window instead.
func sameToolDataShape(a, b any) bool {
	switch x := a.(type) {
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for key, v := range x {
			other, ok := y[key]
			if !ok || !sameToolDataShape(v, other) {
				return false
			}
		}
	case []any:
		y, ok := b.([]any)
		if !ok {
			return false
		}
		if toolDataStrings(x) && toolDataStrings(y) {
			return true
		}
		if len(x) != len(y) {
			return false
		}
		for i := range x {
			if !sameToolDataShape(x[i], y[i]) {
				return false
			}
		}
	default:
		return reflect.TypeOf(a) == reflect.TypeOf(b)
	}
	return true
}

func toolDataStrings(items []any) bool {
	for _, item := range items {
		if _, ok := item.(string); !ok {
			return false
		}
	}
	return true
}
