package agent

import (
	"encoding/json"
	"fmt"
	"strings"
)

// A single tool call can spend the whole remaining context.
//
// The usual answer is to truncate and warn, but that is the worse failure:
// the caller has already paid for the prefix by the time it reads the
// warning, and a prefix of a repository-wide grep is rarely where the
// answer was. Refusing costs a few dozen tokens and leaves the budget
// intact for a narrower query.
//
// So an output that would take an unreasonable share of what is left is
// withheld, and the model is told exactly what it would have cost and what
// to do instead. Saying yes is possible — accept_large_output re-runs the
// call and takes the stated price — because sometimes the big output is
// genuinely the answer.

const (
	// oversizedShare is the fraction of the remaining budget one call may
	// take before it has to be asked for.
	oversizedShare = 0.30
	// oversizedCeiling caps it absolutely. A million-token window makes
	// 30% a very large number, and a repo-wide grep found it: the share
	// test passed and the call cost more than most whole sessions.
	oversizedCeiling = 50_000
	// oversizedFloor keeps small windows workable: below this many tokens
	// a result is never withheld for size alone.
	oversizedFloor = 4_000
)

// AcceptLargeOutput is the argument a model sets to take the stated cost.
const AcceptLargeOutput = "accept_large_output"

// oversized decides whether a result is too expensive to return unasked.
// budget is the context window; used is what the conversation already
// holds. It returns the message to send instead, or "".
func oversized(name, subject, out string, used, budget int, accepted bool) string {
	if accepted || budget <= 0 {
		return ""
	}
	room := budget - used
	if room <= 0 {
		return fmt.Sprintf("%s produced %s and there is no room left for it. Compact the conversation or ask something narrower; %s cannot help here because there is nothing to spend.",
			name, tokensPhrase(out), AcceptLargeOutput)
	}
	cap := int(float64(room) * oversizedShare)
	if cap > oversizedCeiling {
		cap = oversizedCeiling
	}
	if cap < oversizedFloor {
		cap = oversizedFloor
	}
	cost := estTokens(out)
	if cost <= cap {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s was not returned: it is %s, which is %.0f%% of the room left in this conversation.\n\n",
		labelFor(name, subject), tokensPhrase(out), 100*float64(cost)/float64(room))
	b.WriteString("Ask for less — a narrower pattern, a specific path, a line range, or a command that counts or filters instead of printing everything. ")
	fmt.Fprintf(&b, "If you genuinely need all of it, set %s and make the same call again to accept the cost.", AcceptLargeOutput)
	return b.String()
}

func labelFor(name, subject string) string {
	if subject = strings.TrimSpace(subject); subject != "" {
		return name + " " + subject
	}
	return name
}

// estTokens is the same rough measure the context estimate uses; being
// exact here would cost more than it saves.
func estTokens(s string) int { return len(s) / 4 }

func tokensPhrase(s string) string {
	t := estTokens(s)
	if t >= 1000 {
		return fmt.Sprintf("about %.0fk tokens", float64(t)/1000)
	}
	return fmt.Sprintf("about %d tokens", t)
}

// tookLargeOutput reports whether the call opted in.
func tookLargeOutput(input json.RawMessage) bool {
	var in map[string]any
	if json.Unmarshal(input, &in) != nil {
		return false
	}
	v, ok := in[AcceptLargeOutput].(bool)
	return ok && v
}

// takeLargeOutput removes the opt-in from a tool input. The field is
// deliberately absent from every schema: it is needed on a small fraction
// of calls and a property declared on every tool is paid for on every
// request forever, which measured at 21% more context when it and the
// intent field were both added. The refusal that needs it names it, which
// is the only place it is relevant.
func takeLargeOutput(input json.RawMessage) json.RawMessage {
	var obj map[string]json.RawMessage
	if len(input) == 0 || json.Unmarshal(input, &obj) != nil {
		return input
	}
	if _, ok := obj[AcceptLargeOutput]; !ok {
		return input
	}
	delete(obj, AcceptLargeOutput)
	out, err := json.Marshal(obj)
	if err != nil {
		return input
	}
	return out
}

// WithLargeOutputOption adds the opt-in to a tool's schema. Unused by
// default; kept for a caller that would rather declare it than rely on the
// refusal to name it.
func WithLargeOutputOption(schema json.RawMessage) json.RawMessage {
	const prop = `"` + AcceptLargeOutput + `":{"type":"boolean","description":"Re-run accepting the stated token cost of a withheld result."}`
	var obj map[string]json.RawMessage
	if json.Unmarshal(schema, &obj) != nil {
		return schema
	}
	props, ok := obj["properties"]
	if !ok {
		return schema
	}
	t := strings.TrimSpace(string(props))
	if !strings.HasPrefix(t, "{") || !strings.HasSuffix(t, "}") || strings.Contains(t, AcceptLargeOutput) {
		return schema
	}
	inner := strings.TrimSpace(t[1 : len(t)-1])
	if inner == "" {
		obj["properties"] = json.RawMessage("{" + prop + "}")
	} else {
		obj["properties"] = json.RawMessage("{" + prop + "," + inner + "}")
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return schema
	}
	return out
}
