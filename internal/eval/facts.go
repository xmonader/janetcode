package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

// Fact survival. Pass rate says whether a task got done; it does not say
// what the model still knows after its history was compacted. This probes
// that directly: facts are mined from the tool outputs compaction removed
// (the shadowed events of the log — exact, machine-verified strings), the
// model is asked for each one against its post-compaction history, and
// the answer is scored by exact substring. The probe agent has only
// expand_output, so what it recalls it either kept or fetched back.

// Probe is one fact question with its verified answer.
type Probe struct {
	Question string `json:"question"`
	Want     string `json:"want"`
	Got      string `json:"got,omitempty"`
	Correct  bool   `json:"correct"`
	Unknown  bool   `json:"unknown"`  // the model said it could not find it
	Fetched  bool   `json:"fetched"`  // it called expand_output, or faulted with NEED
	Grounded bool   `json:"grounded"` // the answer appears verbatim in what it fetched
}

// FactProbe is the outcome of probing one run.
type FactProbe struct {
	Asked    int     `json:"asked"`
	Correct  int     `json:"correct"`
	Unknown  int     `json:"unknown"`
	Fetched  int     `json:"fetched"`
	Grounded int     `json:"grounded"`
	Probes   []Probe `json:"probes,omitempty"`
}

var (
	reExit     = regexp.MustCompile(`\[exit code (\d+)\]`)
	reNumbered = regexp.MustCompile(`^(\d+)\t(.*)$`)
	reUnknown  = regexp.MustCompile(`(?i)\b(unknown|don't know|do not know|not available|cannot find|could not find|couldn't find|no longer have|not in my context)\b`)
)

// MineFacts turns the compacted-away tool results in events into probes:
// a bash command's exit code and last output line, a read file's numbered
// line. Only results the log shadowed count — what the model still has
// verbatim is not a test of compaction. At most max probes, earliest
// first, since the oldest facts are the ones a summary chain loses.
func MineFacts(events []session.Event, max int) []Probe {
	hidden := session.Shadowed(events)
	// The per-call tool/result events are log-only; the surface event that
	// carried them to the model is the one that gets shadowed. Map each
	// call id to whether its surface message was compacted away.
	gone := map[string]bool{}
	for _, e := range events {
		if e.Surface && e.Message != nil && hidden[e.Seq] {
			for _, b := range e.Message.Blocks {
				if b.Type == llm.BlockToolResult {
					gone[b.ToolUseID] = true
				}
			}
		}
	}
	// A command run more than once has more than one "last line"; the
	// question would be ambiguous, so only commands run once are asked about.
	runs := map[string]int{}
	for _, e := range events {
		if e.Type == session.TypeToolCall && e.Name == "bash" {
			runs[e.Arguments]++
		}
	}
	var out []Probe
	for _, e := range events {
		if e.Type != session.TypeToolResult || e.CallID == "" || !gone[e.CallID] || e.IsError && e.Name != "bash" {
			continue
		}
		if e.Name == "bash" && runs[e.Arguments] > 1 {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(e.Output), "[pruned:") {
			continue
		}
		var in struct {
			Path    string `json:"path"`
			Command string `json:"command"`
		}
		_ = json.Unmarshal([]byte(e.Arguments), &in)
		switch e.Name {
		case "bash":
			if in.Command == "" {
				continue
			}
			if m := reExit.FindStringSubmatch(e.Output); m != nil {
				out = append(out, Probe{Question: fmt.Sprintf("Earlier in this session the command `%s` was run. What exit code did it return? Reply with just the number.", firstLine(in.Command)), Want: m[1]})
			}
			if last := lastOutputLine(e.Output); len(last) >= 12 {
				out = append(out, Probe{Question: fmt.Sprintf("Earlier in this session the command `%s` was run. Quote, exactly, the last line of its output (ignoring the exit-code marker).", firstLine(in.Command)), Want: last})
			}
		case "read_file":
			if in.Path == "" {
				continue
			}
			if n, line := distinctiveLine(e.Output); n > 0 {
				out = append(out, Probe{Question: fmt.Sprintf("Earlier in this session you read the file %s. Quote, exactly, what was on line %d of it.", in.Path, n), Want: line})
			}
		}
		if max > 0 && len(out) >= max {
			break
		}
	}
	if max > 0 && len(out) > max {
		out = out[:max]
	}
	return out
}

func lastOutputLine(out string) string {
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if l == "" || reExit.MatchString(l) || strings.HasPrefix(l, "[") {
			continue
		}
		return l
	}
	return ""
}

// distinctiveLine picks a numbered line of a read_file result worth
// asking about: long enough to be a real fact, short enough to quote.
func distinctiveLine(out string) (int, string) {
	bestN, best := 0, ""
	for _, l := range strings.Split(out, "\n") {
		m := reNumbered.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		text := strings.TrimSpace(m[2])
		if len(text) < 24 || len(text) > 100 || strings.HasPrefix(text, "//") || strings.HasPrefix(text, "#") {
			continue
		}
		if len(text) > len(best) {
			bestN, _ = strconv.Atoi(m[1])
			best = text
		}
	}
	return bestN, best
}

func normWS(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }

// Score marks a probe against the model's reply. excerpts is what the
// model fetched on the way; a correct answer that appears in them is
// grounded — read back, not remembered or reconstructed.
func Score(p *Probe, reply, excerpts string) {
	p.Got = strings.TrimSpace(reply)
	p.Correct = strings.Contains(normWS(p.Got), normWS(p.Want))
	p.Unknown = !p.Correct && reUnknown.MatchString(p.Got)
	p.Grounded = p.Correct && excerpts != "" && strings.Contains(normWS(excerpts), normWS(p.Want))
}

// ProbeFacts asks the agent each probe against its current history. Every
// probe runs on a fresh copy of the agent's messages with expand_output as
// the only tool, so probes do not see each other and cannot change the
// tree. blind withholds the tool: the model must answer from context.
func ProbeFacts(ctx context.Context, ag *agent.Agent, probes []Probe, blind bool) FactProbe {
	fp := FactProbe{}
	for i := range probes {
		p := &probes[i]
		var tools []tool.Tool
		if !blind && ag.Archive != nil {
			tools = append(tools, &agent.ExpandOutput{Archive: ag.Archive})
		}
		clone := &agent.Agent{
			Provider:      ag.Provider,
			Model:         ag.Model,
			MaxTokens:     ag.MaxTokens,
			System:        ag.System,
			Tools:         tool.NewRegistry(tools...),
			Messages:      append([]llm.Message(nil), ag.Messages...),
			MaxIterations: 6,
			AutoApprove:   true,
			Archive:       ag.Archive,
			CompactAt:     -1,
		}
		var out, excerpts strings.Builder
		var mu sync.Mutex
		fetched := false
		_ = clone.Run(ctx, p.Question+" If you cannot find it in your context or the archive, reply UNKNOWN.", func(e agent.Event) {
			mu.Lock()
			defer mu.Unlock()
			switch e.Kind {
			case agent.EvText:
				out.WriteString(e.Text)
			case agent.EvToolRun:
				fetched = true
			case agent.EvToolResult:
				excerpts.WriteString(e.Output + "\n")
			case agent.EvNotice:
				if strings.HasPrefix(e.Text, "archive fault") {
					fetched = true
				}
			}
		})
		// A NEED round hands excerpts back as a user message; count them.
		for _, m := range clone.Messages[len(ag.Messages):] {
			if m.Role == llm.RoleUser && strings.HasPrefix(m.Text(), "[Archive excerpts") {
				excerpts.WriteString(m.Text() + "\n")
			}
		}
		Score(p, out.String(), excerpts.String())
		p.Fetched = fetched
		fp.Asked++
		if p.Correct {
			fp.Correct++
		}
		if p.Unknown {
			fp.Unknown++
		}
		if fetched {
			fp.Fetched++
		}
		if p.Grounded {
			fp.Grounded++
		}
	}
	fp.Probes = probes
	return fp
}
