package secret

import (
	"encoding/json"
	"sort"
	"strings"
	"sync"
)

// minRegistered is the shortest value Register keeps: replacing a one or
// two character string would mangle every line of output it appears in,
// and such a value is not a secret worth the name.
const minRegistered = 4

var (
	regMu   sync.RWMutex
	regVals = map[string]bool{}
)

// Register makes value a secret this process must never let through
// Redact or Scan: a value the user typed into a secret prompt has no
// recognisable shape, so no pattern would ever find it in a command's
// output. The JSON-escaped spelling is registered too, since a tool that
// prints JSON escapes quotes and backslashes. It reports false for a
// value too short to be redacted safely.
func Register(value string) bool {
	if len(value) < minRegistered {
		return false
	}
	regMu.Lock()
	defer regMu.Unlock()
	regVals[value] = true
	if b, err := json.Marshal(value); err == nil {
		if esc := string(b[1 : len(b)-1]); esc != value {
			regVals[esc] = true
		}
	}
	return true
}

// Forget drops every registered value (tests, and a session that ends).
func Forget() {
	regMu.Lock()
	regVals = map[string]bool{}
	regMu.Unlock()
}

// redactRegistered replaces every registered value in text, longest
// first so a value that contains another is not left half showing, and
// reports how many it replaced.
func redactRegistered(text string) (string, int) {
	regMu.RLock()
	if len(regVals) == 0 {
		regMu.RUnlock()
		return text, 0
	}
	vals := make([]string, 0, len(regVals))
	for v := range regVals {
		vals = append(vals, v)
	}
	regMu.RUnlock()
	sort.Slice(vals, func(i, j int) bool { return len(vals[i]) > len(vals[j]) })
	n := 0
	for _, v := range vals {
		if c := strings.Count(text, v); c > 0 {
			text = strings.ReplaceAll(text, v, Placeholder)
			n += c
		}
	}
	return text, n
}
