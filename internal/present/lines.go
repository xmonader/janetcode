package present

import (
	"strings"

	"github.com/xmonader/janetcode/internal/evidence"
)

// Tone is how a verdict line is coloured: it passed, it is a caution, or
// it failed. The front-ends pick the colours; the reading is here.
type Tone string

const (
	ToneOK   Tone = "ok"
	ToneWarn Tone = "warn"
	ToneBad  Tone = "bad"
)

// VerdictTone reads a verdict's tone off its mark. The terminal coloured
// three ways and the browser two, so an "✗ tests fail" verdict was red in
// one and grey in the other.
func VerdictTone(text string) Tone {
	switch {
	case strings.HasPrefix(text, "✓"):
		return ToneOK
	case strings.HasPrefix(text, "✗"):
		return ToneBad
	}
	return ToneWarn
}

// Evidence is the evidence model's line under a verdict (the report's
// own Line, the same text every front end shows), with paths shortened
// like the verdict's. ok is false when there is nothing to say. The line
// starts with the status mark, so VerdictTone reads its colour.
func Evidence(cwd string, r *evidence.Report) (text string, ok bool) {
	if r == nil {
		return "", false
	}
	line := r.Line()
	if line == "" {
		return "", false
	}
	return ShortenPaths(cwd, line), true
}

// Verdict is the verdict line as shown: paths inside the project are
// relative and the home directory is ~.
func Verdict(cwd, text string) string { return ShortenPaths(cwd, text) }

// Routine reports a notice that says the harness did what it does on
// every turn, such as attaching the repository map. It stays in the
// session log and the trajectory; in the conversation it was a line of
// noise under every prompt, so the front ends leave it out.
func Routine(text string) bool {
	return strings.HasPrefix(text, "repository map attached")
}

// Notice is a harness notice as shown, with paths shortened the same way.
func Notice(cwd, text string) string { return ShortenPaths(cwd, text) }

// Compacted is the note that the conversation was compacted, with where
// the archived output went.
func Compacted(text string) string {
	return text + " · expand_output brings archived output back; /context shows what remains"
}

// GoalEnded is the line that says a /goal is finished or blocked — the
// one line the user is waiting for, so it is not left in a tool's output.
func GoalEnded(output string) string {
	return strings.TrimSpace(output) + " · nothing more runs for it; /goal starts another"
}
