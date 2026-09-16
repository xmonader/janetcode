package llm

import "strings"

// Prompt caching is all about where the stable prefix ends. Everything a
// provider is asked to cache has to be byte-identical from one turn to
// the next, or the cache write is paid for and never read; everything
// after the breakpoint is free to change. The system prompt is where
// that line falls, and it fell in two places — one in each provider —
// which is how the two could disagree about what is stable.
//
// This is the one description of it. internal/prompt writes the volatile
// facts (working directory, date, git state, project instructions) under
// the EnvSection heading and nothing volatile above it, so the split is
// exactly there.

// EnvSection is the heading internal/prompt.System puts the session's
// volatile facts under. Everything above it is identical across working
// directories and days; everything from it changes per session and must
// stay outside the cached prefix.
const EnvSection = "# Environment\n"

// StablePrefix splits a system prompt at EnvSection: stable is the part
// worth a cache breakpoint, volatile the rest. A prompt with no such
// heading, or one that begins with it, has no stable part to cache and
// comes back as stable "" and volatile = system, which callers read as
// "cache the whole thing or nothing, as the dialect prefers".
func StablePrefix(system string) (stable, volatile string) {
	i := strings.Index(system, EnvSection)
	if i <= 0 {
		return "", system
	}
	return system[:i], system[i:]
}
