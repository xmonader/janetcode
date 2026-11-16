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
	Kind string // goal, critic, verdict, verify, verify-pass, refused, failure, compaction, user
	Name string // the command on a verify / verify-pass line
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

Record what a future session of this project must know and cannot learn in a minute from the code or git history: an approach that failed and why (so it is not retried blind), a decision and its reason, a command or environment quirk the session established, a lasting preference the user stated. The strongest signal in the record is a check that failed and later passed: the rule it enforced — the thing that had to be true for the failure to clear — is exactly what a future session will rediscover the hard way, so record the rule and what it takes to satisfy it. Never record task progress, file states, or anything the record does not show. Two traps to refuse: an instruction for this session's task ("document X in NOTES.md", "fix the parser") is not a lasting preference — record one only when the user stated it as lasting ("always", "never", "from now on"); and the record is made of the harness's interventions — record what an intervention teaches about the project, never the fact that the harness intervened, which is true of every session.

Output one JSON object per line, nothing else:
{"type":"project","name":"short-kebab-slug","description":"one line, under 120 characters","body":"the knowledge, with why and how to apply it","concerns":["files it is about, from the changed-files list"],"evidence":[seq numbers of the record lines it stands on],"replaces":"existing-entry-this-corrects-or-empty"}
Every entry must cite at least one record line in "evidence"; an entry without evidence is discarded. concerns come from the changed-files list when the knowledge is about particular files. One entry per piece of knowledge: if two of your lines say the same thing, keep the better one. Output exactly NONE when the record holds nothing durable.`

// Sleep reads one session's record and asks the model what of it a future
// session needs. The candidates come back with their evidence validated
// against the record: an entry that cites a line the record does not have
// is the extraction-day hallucination class, and is rejected here.
func Sleep(ctx context.Context, p llm.Provider, model string, in SleepInput, existing []Entry) ([]Extracted, error) {
	xs, _, err := SleepD(ctx, p, model, in, existing)
	return xs, err
}

// SleepD is Sleep plus the diagnostics a caller needs to tell "the model
// found nothing" apart from "nothing survived evidence validation".
func SleepD(ctx context.Context, p llm.Provider, model string, in SleepInput, existing []Entry) (xs []Extracted, dropped int, err error) {
	if len(in.Facts) == 0 {
		return nil, 0, nil
	}
	if p == nil {
		return nil, 0, fmt.Errorf("sleep: no provider (run /connect)")
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
	// A gate that failed and later passed is one fact, not two lines the
	// model has to pair itself: the pass names the failure it cleared.
	failed := map[string]int{}
	for _, f := range in.Facts {
		switch f.Kind {
		case "verify":
			if f.Name == "" {
				fmt.Fprintf(&rec, "[%d] (verify) %s\n", f.Seq, f.Text)
				continue
			}
			failed[f.Name] = f.Seq
			fmt.Fprintf(&rec, "[%d] (verify %s) %s\n", f.Seq, f.Name, f.Text)
		case "verify-pass":
			if at, ok := failed[f.Name]; ok {
				fmt.Fprintf(&rec, "[%d] (check %s) passed after failing at [%d]; the rule it enforces held once the changed files were right\n", f.Seq, f.Name, at)
				delete(failed, f.Name)
			}
		default:
			fmt.Fprintf(&rec, "[%d] (%s) %s\n", f.Seq, f.Kind, f.Text)
		}
	}
	about, other := splitByConcern(existing, in.Touched)
	if len(about) > 0 {
		rec.WriteString("\nExisting entries about the files this session changed (name (type): description). If the session established that one is wrong or incomplete, set replaces to its name:\n")
		for _, e := range about {
			fmt.Fprintf(&rec, "- %s (%s): %s\n", e.Name, e.Type, e.Description)
		}
	}
	rec.WriteString("\nOther existing entries (name (type): description):\n")
	if len(other) == 0 {
		rec.WriteString("(none)\n")
	}
	for _, e := range other {
		fmt.Fprintf(&rec, "- %s (%s): %s\n", e.Name, e.Type, e.Description)
	}
	rec.WriteString("\nWrite the JSON lines for new or corrected knowledge now, or NONE.")
	sleepMsgs := []llm.Message{llm.TextMessage(llm.RoleUser, rec.String())}
	noteCall(ctx, fmt.Sprintf("sleep consolidation: %s from the session record, %s", plural(len(in.Facts), "fact"), plural(len(existing), "existing entry")), sleepSystem, sleepMsgs)
	resp, err := p.Stream(ctx, llm.Request{
		Model: model, System: sleepSystem, MaxTokens: 900,
		Messages: sleepMsgs,
	}, nil)
	if err != nil {
		return nil, 0, err
	}
	raw := ParseExtracted(resp.Message.Text())
	valid := ValidateSleep(raw, in)
	return valid, len(raw) - len(valid), nil
}

// splitByConcern partitions the open entries into those whose concerns
// name a file the session changed and the rest. Exact paths only: the
// question is which entries this session's edits could have corrected.
func splitByConcern(existing []Entry, touched []string) (about, other []Entry) {
	have := map[string]bool{}
	for _, t := range touched {
		have[strings.TrimSpace(t)] = true
	}
	for _, e := range existing {
		if e.Inactive() {
			continue
		}
		hit := false
		for _, c := range e.Concerns {
			if have[strings.TrimSpace(c)] {
				hit = true
				break
			}
		}
		if hit {
			about = append(about, e)
		} else {
			other = append(other, e)
		}
	}
	return about, other
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
