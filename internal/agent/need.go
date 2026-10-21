package agent

import (
	"fmt"
	"regexp"
	"strings"
)

// The NEED protocol: the harness half of epistemic faulting. A model that
// lacks an exact value after compaction is told never to guess it but to
// declare the need — by calling expand_output, or, when it answers in
// text instead, with a line "NEED: <query>". Those lines are caught here:
// each query is run against the archive, the excerpts are handed back as
// a user message, and the model is asked again, up to maxNeedRounds
// times. Measured elsewhere, this turns ~1% recall of compacted facts
// into ~60%; the same study found a harness that let confident answers
// through unchallenged lost a third of that.

var needRe = regexp.MustCompile(`(?im)^\s*NEED\s*[:：]\s*(.+?)\s*$`)

const (
	maxNeedRounds = 2
	needBudget    = 4_000 // bytes of excerpts per query
)

// faultNeeds resolves the NEED lines in text against the archive and
// appends the excerpts for another model call. It reports whether the
// turn should continue.
func (a *Agent) faultNeeds(text string, rounds *int, emit func(Event)) bool {
	if a.Archive == nil || a.Off["faults"] || *rounds >= maxNeedRounds {
		return false
	}
	var needs []string
	seen := map[string]bool{}
	for _, m := range needRe.FindAllStringSubmatch(text, -1) {
		q := strings.TrimSpace(m[1])
		if q == "" || seen[strings.ToLower(q)] {
			continue
		}
		seen[strings.ToLower(q)] = true
		needs = append(needs, q)
	}
	if len(needs) == 0 {
		return false
	}
	*rounds++
	var sb strings.Builder
	sb.WriteString("[Archive excerpts for your NEED queries]\n")
	for _, q := range needs {
		fmt.Fprintf(&sb, "\n--- NEED: %s ---\n%s\n", q, searchArchive(a.Archive, nil, q, needBudget))
	}
	sb.WriteString("\nContinue with these. Quote exact values from the excerpts; if a value is truly absent from them, say UNKNOWN for it rather than guessing. You may also call expand_output with an id and offset/limit to read around a match.")
	emit(Event{Kind: EvNotice, Text: fmt.Sprintf("archive fault (%d/%d): %s", *rounds, maxNeedRounds, strings.Join(needs, "; "))})
	a.appendHarnessText(sb.String())
	return true
}
