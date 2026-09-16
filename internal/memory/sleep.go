package memory

import (
	"context"
	"fmt"
	"strings"

	"github.com/xmonader/janetcode/internal/llm"
)

// SleepFact is one line of a session's structured record: what the
// harness itself observed, with the event's seq in the session log.
type SleepFact struct {
	Seq  int
	Kind string // goal, critic, verdict, refused, notice, compaction, user
	Text string
}

// SleepInput is what one session left behind, folded from its event log
// by the caller (agent.FoldSleep). It is the record, never prose: no
// 14-message window, no harness text posing as the user.
type SleepInput struct {
	SessionID   string
	Goal        string
	GoalOutcome string // "complete", "blocked" or ""
	Facts       []SleepFact
	// Touched are the files the session wrote or edited; entries about
	// them name them in concerns so a later turn touching the same files
	// recalls what this one learned.
	Touched []string
}

const sleepSystem = `You consolidate what a coding agent learned from one session, for the sessions that come after it. Your input is the session's machine-written record: its goal and outcome, the harness's interventions (a verify gate that failed, a critic warning, a refused tool call, a compaction), the user's requests, and the files the session changed. It is a record, not a conversation: never invent beyond it.

Record what a future session of this project must know and cannot learn in a minute from the code or git history: an approach that failed and why (so it is not retried blind), a decision and its reason, a command or environment quirk the session established, a lasting preference the user stated. Never record task progress, file states, or anything the record does not show.

Output one JSON object per line, nothing else:
{"type":"project","name":"short-kebab-slug","description":"one line, under 120 characters","body":"the knowledge, with why and how to apply it","concerns":["files it is about, from the changed-files list"],"evidence":[seq numbers of the record lines it stands on],"replaces":"existing-entry-this-corrects-or-empty"}
Every entry must cite at least one record line in "evidence"; an entry without evidence is discarded. concerns come from the changed-files list when the knowledge is about particular files. Output exactly NONE when the record holds nothing durable.`

// Sleep reads one session's record and asks the model what of it a future
// session needs. The candidates come back with their evidence validated
// against the record: an entry that cites a line the record does not have
// is the extraction-day hallucination class, and is rejected here.
func Sleep(ctx context.Context, p llm.Provider, model string, in SleepInput, existing []Entry) ([]Extracted, error) {
	if len(in.Facts) == 0 {
		return nil, nil
	}
	var rec strings.Builder
	fmt.Fprintf(&rec, "Session %s.\n", in.SessionID)
	if in.Goal != "" {
		fmt.Fprintf(&rec, "Goal: %s (outcome: %s)\n", in.Goal, orEmpty(in.GoalOutcome))
	}
	if len(in.Touched) > 0 {
		fmt.Fprintf(&rec, "Changed files: %s\n", strings.Join(in.Touched, ", "))
	}
	rec.WriteString("Record:\n")
	for _, f := range in.Facts {
		fmt.Fprintf(&rec, "[%d] (%s) %s\n", f.Seq, f.Kind, f.Text)
	}
	rec.WriteString("\nExisting memory entries (name (type): description):\n")
	if len(existing) == 0 {
		rec.WriteString("(none)\n")
	}
	for _, e := range existing {
		if e.Closed() {
			continue
		}
		fmt.Fprintf(&rec, "- %s (%s): %s\n", e.Name, e.Type, e.Description)
	}
	rec.WriteString("\nWrite the JSON lines for new or corrected knowledge now, or NONE.")
	resp, err := p.Stream(ctx, llm.Request{
		Model: model, System: sleepSystem, MaxTokens: 900,
		Messages: []llm.Message{llm.TextMessage(llm.RoleUser, rec.String())},
	}, nil)
	if err != nil {
		return nil, err
	}
	return ValidateSleep(ParseExtracted(resp.Message.Text()), in), nil
}

func orEmpty(s string) string {
	if s == "" {
		return "no goal"
	}
	return s
}

// ValidateSleep keeps the candidates whose cited record lines exist, and
// rewrites the model's bare seq numbers into the store's evidence form
// "<session>#<seq>". A candidate with an evidence field but no surviving
// citation is dropped, and one with no evidence field at all is dropped
// too: sleep entries always say where they were learned.
func ValidateSleep(xs []Extracted, in SleepInput) []Extracted {
	valid := map[int]bool{}
	for _, f := range in.Facts {
		valid[f.Seq] = true
	}
	var out []Extracted
	for _, x := range xs {
		if len(x.EvidenceSeqs) == 0 {
			continue
		}
		var ev []string
		ok := true
		for _, n := range x.EvidenceSeqs {
			if !valid[n] {
				ok = false
				break
			}
			ev = append(ev, fmt.Sprintf("%s#%d", in.SessionID, n))
		}
		if !ok {
			continue
		}
		x.Evidence = ev
		x.EvidenceSeqs = nil
		out = append(out, x)
	}
	return out
}
