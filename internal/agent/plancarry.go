package agent

import (
	"encoding/json"

	"github.com/xmonader/janetcode/internal/llm"
)

// A reply that reasoned at length and then started writing product files
// is followed, once per turn, by a note that its reasoning is gone and the
// brief sections the next file implements should be re-read from the
// conversation. Replayed reasoning is stripped on the next request, so the
// reply that writes the core file later sees none of the plan. hero_agent's
// effbench ab-carry (3 tasks x 4 seeds): no note 10/12 with 525k input per
// run, the re-read note 12/12 with 382k, the least fresh input of the arms
// (6 of 8 hidden-test misses before it were the core file written cold,
// from a plan whose text had stated the rule the code then broke). The
// note points at the copy of the brief already in the history; saying
// "do not read the file again" cut the extra read_file of the brief.
// It rides with the step's tool results, the one place a note may go
// between a tool call and its answer. JANETCODE_OFF=plancarry turns it off.
//
// A reply whose reasoning is sent back (llm.Block.Replayed: Kimi's
// "reasoning" field, an Anthropic signed block) still has its plan, so it
// gets no note: the note would tell the model something false.

// planCarryTokens is how much reasoning makes a reply a plan to carry
// (hero_agent: 6000 chars, about 1500 tokens).
const planCarryTokens = 1500

const planCarryNote = "[harness] Your earlier reasoning is no longer visible to you. Before " +
	"writing the next file, re-read the brief sections it implements in this conversation, " +
	"where you already read them; do not read the file again."

// planCarryNote returns the note for the step that ran resp's calls, or "".
func (a *Agent) planCarryNote(resp *llm.Response) string {
	if a.Off["plancarry"] || a.pl.planCarried {
		return ""
	}
	// Adaptive: only when the plan named product files still unwritten,
	// the shape where a later reply writes one cold (risk.go).
	if a.Adaptive && len(plannedUnwritten(a.Messages)) == 0 {
		return ""
	}
	if !isPlanReply(resp.Message, resp.Usage) || (planReplayed(resp.Message) && !a.Off["reasoning"]) {
		return ""
	}
	a.pl.planCarried = true
	return planCarryNote
}

// isPlanReply: the reply reasoned at least planCarryTokens and made an
// edit to a file that is not a test. A reply that only read files or ran
// checks is not a plan worth carrying.
func isPlanReply(m llm.Message, u llm.Usage) bool {
	think := 0
	for _, b := range m.Blocks {
		if b.Type == llm.BlockThinking {
			think += len(b.Text) / 4
		}
	}
	if u.ReasoningTokens > think {
		think = u.ReasoningTokens
	}
	if think < planCarryTokens {
		return false
	}
	for _, u := range m.ToolUses() {
		if !editTool(u.Name) {
			continue
		}
		var in struct {
			Path string `json:"path"`
		}
		if json.Unmarshal(u.Input, &in) == nil && in.Path != "" && !isTestFile(in.Path) {
			return true
		}
	}
	return false
}

// planReplayed reports whether m's reasoning goes back to the provider.
func planReplayed(m llm.Message) bool {
	for _, b := range m.Blocks {
		if b.Replayed() {
			return true
		}
	}
	return false
}
