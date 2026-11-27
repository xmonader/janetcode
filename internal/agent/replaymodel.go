package agent

import (
	"path"
	"strings"
)

// replayFamilies are the model families that do better with their earlier
// reasoning sent back. Most do not: on DeepSeek, Kimi, GLM and gpt-oss the
// replayed reasoning cost 29-74% more input for the same passes, so replay
// is off by default. Inkling is the exception: without its reasoning it
// re-derives its plan on the next call, so ink-replay measured 13.1 calls
// and 24k output per run with replay against 17.7 and 36k without.
var replayFamilies = []string{"inkling*"}

// ReplayModel reports whether a model id belongs to a family that replays
// reasoning by default, whatever provider prefix the id carries. extra are
// further path.Match globs from the configuration (replay_models).
func ReplayModel(id string, extra ...string) bool {
	id = strings.ToLower(strings.TrimSpace(id))
	if i := strings.LastIndex(id, "/"); i >= 0 {
		id = id[i+1:]
	}
	for _, p := range append(append([]string(nil), replayFamilies...), extra...) {
		if ok, _ := path.Match(strings.ToLower(p), id); ok {
			return true
		}
	}
	return false
}
