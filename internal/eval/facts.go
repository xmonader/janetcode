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
	"github.com/xmonader/janetcode/internal/textfmt"
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
	// InArchive says the value is still on disk, so it could in principle
	// be recovered; Retrieved says the model's own searches actually
	// returned it. Together with Correct they split a miss three ways:
	// the value was gone, the search did not find it, or the model had it
	// and did not use it.
	InArchive bool `json:"in_archive"`
	Retrieved bool `json:"retrieved"`
}

// FactProbe is the outcome of probing one run.
type FactProbe struct {
	Asked     int     `json:"asked"`
	Correct   int     `json:"correct"`
	Unknown   int     `json:"unknown"`
	Fetched   int     `json:"fetched"`
	Grounded  int     `json:"grounded"`
	InArchive int     `json:"in_archive"`
	Retrieved int     `json:"retrieved"`
	Probes    []Probe `json:"probes,omitempty"`
}

// distinctiveMax is how often an answer may appear in the session before
// it is too common to identify anything (ctxresearch uses the same bound).
const distinctiveMax = 3

var (
	reExit     = regexp.MustCompile(`\[exit code (\d+)\]`)
	reNumbered = regexp.MustCompile(`^(\d+)\t(.*)$`)
	reErrorish = regexp.MustCompile(`(?i)(error|failed|fail:|cannot|not found|undefined|panic|traceback|exception|no such|refused|denied|expected)`)
	reIdent    = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]{4,}`)
	reUnknown  = regexp.MustCompile(`(?i)\b(unknown|don't know|do not know|not available|cannot find|could not find|couldn't find|no longer have|not in my context)\b`)
)

// MineFacts turns the compacted-away tool results in events into probes.
// Two shapes, deliberately: content questions, which name a distinctive
// string from the output and can be answered by searching for what the
// question is about; and positional ones, which ask for a numbered line
// and cannot. A generator that produces only the second measures one
// lookup path and says nothing about the other, which is what an earlier
// run of this did.
//
// A fact must be distinctive to be worth asking: an answer that appears
// all over the session identifies nothing, so one occurring more than
// distinctiveMax times is dropped, as is one that appears in its own
// question. Only results the log shadowed count — what the model still
// has verbatim is not a test of compaction.
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
			Pattern string `json:"pattern"`
		}
		_ = json.Unmarshal([]byte(e.Arguments), &in)
		switch e.Name {
		case "bash":
			if in.Command == "" {
				continue
			}
			if m := reExit.FindStringSubmatch(e.Output); m != nil {
				out = append(out, Probe{Question: fmt.Sprintf("Earlier in this session the command `%s` was run. What exit code did it return? Reply with just the number.", textfmt.FirstLine(in.Command, 0)), Want: m[1]})
			}
			if last := lastOutputLine(e.Output); len(last) >= 12 {
				out = append(out, Probe{Question: fmt.Sprintf("Earlier in this session the command `%s` was run. Quote, exactly, the last line of its output (ignoring the exit-code marker).", textfmt.FirstLine(in.Command, 0)), Want: last})
			}
			// The line a failure was reported on, asked for by what it says
			// rather than by where it was.
			if bad := errorLine(e.Output); bad != "" {
				out = append(out, Probe{
					Question: fmt.Sprintf("Earlier in this session the command `%s` reported a problem. Quote, exactly, the line of its output that says what went wrong.", textfmt.FirstLine(in.Command, 0)),
					Want:     bad,
				})
			}
		case "read_file", "grep":
			subject := in.Path
			if subject == "" {
				subject = in.Pattern
			}
			if subject == "" {
				continue
			}
			// A content question: it names something the answer's line is
			// about, so a search over words can find it. This is the shape
			// ctxresearch mines, and the one a ranked archive answers.
			anchor, anchored := anchoredLine(e.Output)
			if anchor != "" {
				out = append(out, Probe{
					Question: fmt.Sprintf("Earlier in this session you looked at %s. Quote, exactly, the full line there that mentions %s.", subject, anchor),
					Want:     anchored,
				})
			}
			// A positional question: nothing in it matches the answer, so
			// only a direct line lookup can serve it. It asks about a
			// different line from the content probe, so the two shapes
			// test two paths rather than collapsing into one fact.
			if e.Name == "read_file" {
				if n, line := distinctiveLine(e.Output, anchored); n > 0 {
					out = append(out, Probe{Question: fmt.Sprintf("Earlier in this session you read the file %s. Quote, exactly, what was on line %d of it.", subject, n), Want: line})
				}
			}
		}
		if max > 0 && len(out) >= max {
			break
		}
	}
	out = distinctive(out, events)
	if max > 0 && len(out) > max {
		out = out[:max]
	}
	return out
}

// distinctive drops probes whose answer is not a usable key: too short,
// contained in its own question, or so common across the session that
// producing it proves nothing.
func distinctive(probes []Probe, events []session.Event) []Probe {
	var all strings.Builder
	for _, e := range events {
		all.WriteString(e.Output)
		all.WriteByte('\n')
		if e.Message != nil {
			all.WriteString(e.Message.Text())
			all.WriteByte('\n')
		}
	}
	text := all.String()
	seen := map[string]bool{}
	var out []Probe
	for _, p := range probes {
		w := strings.TrimSpace(p.Want)
		// Short answers are guessable — an exit code is one of two or
		// three numbers — so producing one is not evidence of recall.
		if len(w) < 8 || seen[w] {
			continue
		}
		if strings.Contains(strings.ToLower(p.Question), strings.ToLower(w)) {
			continue
		}
		if n := strings.Count(text, w); n == 0 || n > distinctiveMax {
			continue
		}
		seen[w] = true
		out = append(out, p)
	}
	return out
}

// anchoredLine picks a line worth asking about by naming something in it:
// an error, or failing that a distinctive identifier. It returns the
// anchor to name in the question and the line to expect back.
func anchoredLine(out string) (string, string) {
	best, anchor := "", ""
	// The score to beat. It used to be compared with len(anchor) — the
	// length of the anchor chosen so far, which is not a score at all: a
	// line with two identifiers could displace an error line worth 11
	// simply because the error line's anchor happened to be a short word.
	bestScore := 0
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(stripNumber(raw))
		if len(line) < 16 || len(line) > 160 {
			continue
		}
		ids := reIdent.FindAllString(line, -1)
		if len(ids) == 0 {
			continue
		}
		// Prefer a line that reports a problem: those are the facts a
		// resumed agent actually needs.
		score := len(ids)
		if reErrorish.MatchString(line) {
			score += 10
		}
		if score <= bestScore {
			continue
		}
		pick := ids[0]
		for _, id := range ids {
			if len(id) > len(pick) {
				pick = id
			}
		}
		// The anchor must not be the whole answer, or the question would
		// contain what it asks for.
		if len(pick) < len(line)/2 {
			best, anchor, bestScore = line, pick, score
		}
	}
	return anchor, best
}

// errorLine is the first line of output that reports a failure.
func errorLine(out string) string {
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(stripNumber(raw))
		if len(line) >= 16 && len(line) <= 200 && reErrorish.MatchString(line) && !strings.HasPrefix(line, "[") {
			return line
		}
	}
	return ""
}

// stripNumber removes a read_file result's "12\t" line-number prefix.
func stripNumber(line string) string {
	if m := reNumbered.FindStringSubmatch(line); m != nil {
		return m[2]
	}
	return line
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
// asking about: long enough to be a real fact, short enough to quote, and
// not the one a content probe already asked about.
func distinctiveLine(out, avoid string) (int, string) {
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
		if avoid != "" && text == avoid {
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

// inArchive reports whether the value is still recoverable at all: it
// scans every archived entry for the text. A miss here is a loss no
// retrieval could have prevented.
func inArchive(ar *agent.Archive, want string) bool {
	if ar == nil || want == "" {
		return false
	}
	w := normWS(want)
	for _, e := range ar.List() {
		_, body, err := ar.Get(e.ID)
		if err != nil {
			continue
		}
		if strings.Contains(normWS(body), w) {
			return true
		}
	}
	return false
}

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
		clone, err := agent.New(agent.Deps{
			Provider: ag.Provider,
			Tools:    tool.NewRegistry(tools...),
		}, agent.Settings{
			Model:         ag.Model,
			MaxTokens:     ag.MaxTokens,
			MaxIterations: 6,
			AutoApprove:   true,
			CompactAt:     -1,
		})
		if err != nil {
			// A probe that could not even be built is reported as its own
			// answer, so the run says so rather than scoring a silent miss.
			p.Got = "probe could not start: " + err.Error()
			continue
		}
		clone.System = ag.System
		clone.Messages = append([]llm.Message(nil), ag.Messages...)
		clone.Archive = ag.Archive
		var out, excerpts strings.Builder
		var mu sync.Mutex
		fetched := false
		_ = clone.Run(ctx, p.Question+" If you cannot find it in your context or the archive, reply UNKNOWN.", func(e agent.Event) {
			mu.Lock()
			defer mu.Unlock()
			switch e.Kind {
			case agent.EvText:
				out.WriteString(e.Text)
			case agent.EvDiscard:
				// The retried attempt replaces these deltas; scoring
				// both would judge the model on text it never sent.
				out.Reset()
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
		p.InArchive = inArchive(ag.Archive, p.Want)
		p.Retrieved = strings.Contains(normWS(excerpts.String()), normWS(p.Want))
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
		if p.InArchive {
			fp.InArchive++
		}
		if p.Retrieved {
			fp.Retrieved++
		}
	}
	fp.Probes = probes
	return fp
}
