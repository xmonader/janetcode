package sleepfold

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/memory"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// Goal phases as the session log records them; the agent package owns
// the constants, and this package must not depend on it (archtest).
const (
	goalComplete = "complete"
	goalBlocked  = "blocked"
)

// sleepFactCap keeps one session's record to what a single consolidation
// call can hold. Notices are the first thing dropped: they are the
// harness talking to itself, and the few that matter (a critic warning, a
// refusal, a verdict) are their own kinds.
const sleepFactCap = 80

// Fold reads a session's event log and returns the structured record
// the memory consolidation pass (memory.Sleep) works from: the goal and
// how it ended, what the harness intervened on, what the user asked, what
// was refused, and which files the session changed. Nothing here is
// generated prose: every line carries the seq of the event it came from,
// which is what lets sleep entries cite evidence that can be checked.
func Fold(events []session.Event) memory.SleepInput {
	var in memory.SleepInput
	seen := map[string]bool{}
	for _, e := range events {
		switch e.Type {
		case session.TypeGoalCreate:
			in.Goal = e.Output
		case session.TypeGoalPhase:
			if e.Reason == goalComplete || e.Reason == goalBlocked {
				in.GoalOutcome = e.Reason
				if e.Output != "" {
					in.Facts = append(in.Facts, memory.SleepFact{Seq: e.Seq, Kind: "goal", Text: e.Reason + ": " + textfmt.Clip(e.Output, 200)})
				}
			}
		case session.TypeCritic:
			in.Facts = append(in.Facts, memory.SleepFact{Seq: e.Seq, Kind: "critic", Text: textfmt.Clip(e.Output, 300)})
		case session.TypeVerdict:
			in.Facts = append(in.Facts, memory.SleepFact{Seq: e.Seq, Kind: "verdict", Text: textfmt.Clip(strings.TrimSpace(e.Name+" "+e.Output), 300)})
		case session.TypeGate:
			// Both halves of the pair InferRules harvests: a failure and
			// the later pass of the same command. Name is the command.
			kind := "verify-pass"
			text := e.Name
			if e.IsError {
				kind = "verify"
				text = textfmt.Clip(e.Output, 300)
				if text == "" {
					text = e.Name
				}
			}
			in.Facts = append(in.Facts, memory.SleepFact{Seq: e.Seq, Kind: kind, Name: e.Name, Text: text})
		case session.TypeCompactSummary:
			in.Facts = append(in.Facts, memory.SleepFact{Seq: e.Seq, Kind: "compaction", Text: fmt.Sprintf("context compacted from about %d tokens", e.Tokens)})
		case session.TypeToolResult:
			switch {
			case e.Denied:
				in.Facts = append(in.Facts, memory.SleepFact{Seq: e.Seq, Kind: "refused", Name: e.Name, Text: textfmt.Clip(strings.TrimSpace(e.Name+": "+firstLines(e.Output, 1)), 300)})
			case e.IsError || failedCommand(e):
				// A command or tool that failed is what the session
				// learned from most directly.
				in.Facts = append(in.Facts, memory.SleepFact{Seq: e.Seq, Kind: "failure", Name: e.Name, Text: textfmt.Clip(strings.TrimSpace(e.Name+": "+firstLines(e.Output, 4)), 300)})
			}
		case session.TypeUserMessage:
			if e.Message == nil {
				continue
			}
			for _, b := range e.Message.Blocks {
				if b.Type == llm.BlockText && !b.Synthetic {
					in.Facts = append(in.Facts, memory.SleepFact{Seq: e.Seq, Kind: "user", Text: textfmt.Clip(firstLines(b.Text, 1), 200)})
					break
				}
			}
		case session.TypeToolCall:
			if e.Name != "write_file" && e.Name != "edit_file" {
				continue
			}
			var args struct {
				Path string `json:"path"`
			}
			if json.Unmarshal([]byte(e.Arguments), &args) == nil && args.Path != "" && !seen[args.Path] {
				seen[args.Path] = true
				in.Touched = append(in.Touched, args.Path)
			}
		}
	}
	if len(in.Facts) > sleepFactCap {
		in.Facts = capSleepFacts(in.Facts)
	}
	return in
}

// failedCommand reports a shell command that exited non-zero. Bash
// results carry that as the verbatim "[exit code N]" line rather than an
// error (tool/exit.go), except where the code is the command's answer
// (grep finding nothing), which is not a failure.
func failedCommand(e session.Event) bool {
	if e.IsError {
		return true
	}
	if e.Name != "bash" && e.Name != "job" {
		return false
	}
	return strings.Contains(e.Output, "[exit code") && !strings.Contains(e.Output, "an answer, not a failure")
}

// capSleepFacts keeps every goal, verdict, critic and refusal line and
// fills the rest of the budget chronologically; overflow drops notices
// and the oldest user lines first.
func capSleepFacts(facts []memory.SleepFact) []memory.SleepFact {
	var kept, rest []memory.SleepFact
	for _, f := range facts {
		switch f.Kind {
		case "goal", "verdict", "critic", "refused", "verify", "verify-pass", "failure", "compaction":
			kept = append(kept, f)
		default:
			rest = append(rest, f)
		}
	}
	if room := sleepFactCap - len(kept); room < len(rest) {
		if room < 0 {
			room = 0
		}
		rest = rest[len(rest)-room:]
	}
	return append(kept, rest...)
}

// firstLines is the head of a result, enough to say what came back.
func firstLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}
