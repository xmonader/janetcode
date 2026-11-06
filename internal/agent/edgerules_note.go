package agent

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/xmonader/janetcode/internal/llm"
)

// On a green turn end, one note per turn names at most three edge rules
// of the brief (edgerules.go) that no check of the turn asserts at their
// edge. A note and not a probe or a prompt rule, for the reasons
// hero_agent measured: its edge probe (a side call writing five checks)
// earned one hidden-test pass in ~128 runs for +155-281 s a campaign, and
// a per-rule checklist made the agent 2.3x slower. This is in-thread (no
// side call, cached prefix) and fires once per turn; what the model does
// with it is its own next step. JANETCODE_OFF=edgerules turns it off.

const maxEdgeRules = 3

// edgeRulesNote is the note for a turn whose checks passed, or "" when
// every edge rule of spec is asserted at its edge or the spec states none.
func edgeRulesNote(specName, spec string, checks []string) string {
	chosen := pickEdgeRules(uncheckedEdgeRules(spec, checks), maxEdgeRules)
	if len(chosen) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[harness] Before you finish: %s states these edge rules and no test of yours asserts them at the edge:\n", specName)
	for i, r := range chosen {
		fmt.Fprintf(&b, "%d. %s\n", i+1, r.short(220))
	}
	b.WriteString("Add one assert for each at its exact edge (both sides of a threshold; for a step \"decided after\" another, an input that nearly meets that earlier step), run the tests once, fix the code if one fails, then finish. Nothing else.")
	return b.String()
}

var briefFileRe = regexp.MustCompile(`(?i)(brief|task|spec|requirements|readme)[^/]*\.(md|txt|rst)$`)

var attachedFileRe = regexp.MustCompile(`(?s)<file path=("(?:[^"\\]|\\.)*")>\n(.*?)</file>\n`)

type attachedFile struct{ path, body string }

// attachedFiles takes the files out of a repository sent whole with the
// first message (tool.SmallRepo); any other text has none.
func attachedFiles(text string) []attachedFile {
	if !strings.HasPrefix(text, "<repository files=") {
		return nil
	}
	var out []attachedFile
	for _, m := range attachedFileRe.FindAllStringSubmatch(text, -1) {
		if p, err := strconv.Unquote(m[1]); err == nil {
			out = append(out, attachedFile{p, m[2]})
		}
	}
	return out
}

// turnSpecAndChecks reads the current turn from the history: the brief
// (the user's request, plus any brief file the turn read) and the checks
// (every command it ran, the test files it wrote).
func turnSpecAndChecks(msgs []llm.Message) (spec string, checks []string) {
	start := 0
	for i, m := range msgs {
		if m.Role != llm.RoleUser {
			continue
		}
		for _, b := range m.Blocks {
			if b.Type == llm.BlockText && !b.Synthetic && strings.TrimSpace(b.Text) != "" {
				start = i
			}
		}
	}
	readPath := map[string]string{}
	var sb strings.Builder
	for _, m := range msgs[start:] {
		for _, b := range m.Blocks {
			switch b.Type {
			case llm.BlockText:
				if m.Role != llm.RoleUser {
					continue
				}
				if !b.Synthetic {
					sb.WriteString(b.Text + "\n\n")
					continue
				}
				// A small repository attached whole: its brief and tests
				// are read as if read_file had opened them, since the
				// model is told not to (a-build-incremental's BRIEF.md
				// never reached the spec, and the spec review stayed off).
				for _, f := range attachedFiles(b.Text) {
					switch {
					case isTestFile(f.path):
						checks = append(checks, f.body)
					case briefFileRe.MatchString(f.path):
						sb.WriteString(f.body + "\n\n")
					}
				}
			case llm.BlockToolUse:
				var in map[string]any
				if json.Unmarshal(b.Input, &in) != nil {
					continue
				}
				path, _ := in["path"].(string)
				switch {
				case b.Name == "read_file":
					readPath[replyID(b)] = path
				case b.Name == "bash":
					if c, _ := in["command"].(string); c != "" {
						checks = append(checks, c)
					}
				case editTool(b.Name) && isTestFile(path):
					for _, v := range in {
						if s, ok := v.(string); ok && s != path {
							checks = append(checks, s)
						}
					}
				}
			case llm.BlockToolResult:
				if p := readPath[b.ToolUseID]; p != "" && !b.IsError {
					if isTestFile(p) {
						checks = append(checks, b.Text)
					} else if briefFileRe.MatchString(p) {
						sb.WriteString(b.Text + "\n\n")
					}
				}
			}
		}
	}
	return sb.String(), checks
}

// edgeRulesGate returns true when it queued the note: the turn ended
// green after its last edit, once per turn.
func (a *Agent) edgeRulesGate(outcome TurnOutcome, noted *bool, emit func(Event)) bool {
	if a.Off["edgerules"] || *noted || !a.riskyTurn() || a.DryRunOn() {
		return false
	}
	switch outcome.Verdict {
	case VerdictVerified, VerdictSelfChecked:
	default:
		return false
	}
	if outcome.Edited == 0 || outcome.EditedAfterCheck || outcome.Failed > 0 && outcome.Passed == 0 {
		return false
	}
	*noted = true
	spec, checks := turnSpecAndChecks(a.Messages)
	if note := edgeRulesNote("the brief", spec, checks); note != "" {
		emit(Event{Kind: EvNotice, Text: "the checks passed; naming brief edge rules no test asserts at the edge"})
		a.appendHarnessText(note)
		// After a spec review the note gets steps of its own: on the
		// review's leftover count d-cronexpr was told to add three
		// asserts and, one step later, that the review was over.
		if a.reviewSteps >= 0 {
			a.reviewSteps = 0
		}
		return true
	}
	return false
}
