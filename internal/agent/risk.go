package agent

import (
	"encoding/json"
	"strings"

	"github.com/xmonader/janetcode/internal/llm"
)

// Adaptive mode (on by default, JANETCODE_ADAPTIVE=0 turns it off): the checks that cost extra model rounds after a turn is already
// green (the plan-carry note, the edge-rules note, the spec review, the
// stale-check nudge) run only on turns shaped like the ones hero_agent's
// effbench 2026-10-08 where-we-lose report found lean runs losing. Every
// hidden-test miss lean took that regular did not came from one of two
// shapes: a cold core write (a reply planned at length, then a LATER reply
// wrote the core file with almost no reasoning, 5 and 9 reasoning tokens
// after 3.6k and 16.6k token plans) or a long brief behind a large change
// (21 rules and 256+ lines). Short briefs and small changes are neither,
// and there the extra rounds were the ~35% more calls herox made than pi.
// With the mode off every turn counts as risky: all checks on, as before.
// Thresholds are hero_agent's turn_risk.rs: a plan is 6000 chars of
// reasoning (~1.5k tokens), a write is cold under 1200 chars (~300
// tokens), a core file is 4000 bytes, a long brief is over 15 rules, a
// large change over 250 product lines. A brief over three times as long
// is risky behind any change: a-build-incremental's 59-rule brief behind
// 221 lines missed "with cache=None behaviour is exactly as today", the
// kind of rule the spec review exists for. The plan-carry note under adaptive
// fires only when the plan named product files not yet written
// (plannedUnwritten), as hero_agent's narrow_note does.

const (
	planThinkChars  = 6_000
	coldThinkChars  = 1_200
	coreWriteBytes  = 4_000
	longBriefRules  = 15
	largeChangeLine = 250
	veryLongBrief   = 3 * longBriefRules
)

var sourceExts = map[string]bool{}

func init() {
	for _, e := range strings.Fields("py rs ts tsx js jsx mjs cjs go java rb c h cc cpp hpp cs kt swift php scala ex exs") {
		sourceExts[e] = true
	}
}

func thinkChars(m llm.Message) int {
	n := 0
	for _, b := range m.Blocks {
		if b.Type == llm.BlockThinking {
			n += len(b.Text)
		}
	}
	return n
}

// productWrite is one product file a reply's edit call writes, with the
// bytes and lines the call carried.
type productWrite struct {
	path         string
	bytes, lines int
}

func productWrites(m llm.Message) []productWrite {
	var out []productWrite
	for _, b := range m.ToolUses() {
		if !editTool(b.Name) {
			continue
		}
		var in map[string]any
		if json.Unmarshal(b.Input, &in) != nil {
			continue
		}
		path, _ := in["path"].(string)
		if path == "" || isTestFile(path) || !sourceExts[strings.TrimPrefix(extOf(path), ".")] {
			continue
		}
		w := productWrite{path: path}
		for k, v := range in {
			if s, ok := v.(string); ok && k != "path" && k != "then_run" {
				w.bytes += len(s)
				w.lines += strings.Count(s, "\n") + 1
			}
		}
		out = append(out, w)
	}
	return out
}

func extOf(p string) string {
	if i := strings.LastIndex(p, "."); i >= 0 && !strings.Contains(p[i:], "/") {
		return p[i:]
	}
	return ""
}

// coldCoreWrite: a core product file written this turn in a reply that
// thought less than coldThinkChars, right after a reply that planned.
func coldCoreWrite(turn []llm.Message) bool {
	var replies []llm.Message
	for _, m := range turn {
		if m.Role == llm.RoleAssistant {
			replies = append(replies, m)
		}
	}
	for i := 1; i < len(replies); i++ {
		if thinkChars(replies[i-1]) < planThinkChars || thinkChars(replies[i]) >= coldThinkChars {
			continue
		}
		for _, w := range productWrites(replies[i]) {
			if w.bytes >= coreWriteBytes {
				return true
			}
		}
	}
	return false
}

// briefRuleCount counts the rule sentences of a brief (edgerules.go's
// block and sentence filters).
func briefRuleCount(spec string) int {
	n := 0
	for _, b := range briefBlocks(spec) {
		for _, s := range sentencesOf(b.text) {
			if !notARule(s) && len(termsOf(s)) >= 3 {
				n++
			}
		}
	}
	return n
}

// riskyTurn reports whether this turn's expensive checks should run.
func (a *Agent) riskyTurn() bool {
	if !a.Adaptive {
		return true
	}
	turn := a.Messages[turnStart(a.Messages):]
	if coldCoreWrite(turn) {
		return true
	}
	lines := 0
	for _, m := range turn {
		if m.Role == llm.RoleAssistant {
			for _, w := range productWrites(m) {
				lines += w.lines
			}
		}
	}
	if lines == 0 {
		return false
	}
	spec, _ := turnSpecAndChecks(a.Messages)
	rules := briefRuleCount(spec)
	return rules > veryLongBrief || lines > largeChangeLine && rules > longBriefRules
}

// namedSourceFiles: source-file names a text mentions, once each, in order.
func namedSourceFiles(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, raw := range strings.FieldsFunc(text, func(r rune) bool { return strings.ContainsRune(" \t\r\n`'\"()[]{}<>,;:*|=", r) }) {
		tok := strings.TrimRight(raw, ".!?")
		i := strings.LastIndex(tok, ".")
		if i <= 0 || !sourceExts[tok[i+1:]] {
			continue
		}
		stem := tok[:i]
		ok := !strings.HasPrefix(stem, "/") && !strings.Contains(stem, "..")
		for _, r := range stem {
			if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || strings.ContainsRune("_-/.", r)) {
				ok = false
			}
		}
		if ok && !seen[tok] {
			seen[tok] = true
			out = append(out, tok)
		}
	}
	return out
}

// plannedUnwritten: product files the turn's latest planning reply (at
// least planThinkChars of reasoning) named and no edit of the turn has
// written. Nil when the latest reply was no plan.
func plannedUnwritten(msgs []llm.Message) []string {
	turn := msgs[turnStart(msgs):]
	var plan *llm.Message
	for i := len(turn) - 1; i >= 0; i-- {
		if turn[i].Role == llm.RoleAssistant {
			plan = &turn[i]
			break
		}
	}
	if plan == nil || thinkChars(*plan) < planThinkChars {
		return nil
	}
	var said strings.Builder
	for _, b := range plan.Blocks {
		if b.Type == llm.BlockThinking || b.Type == llm.BlockText {
			said.WriteString(b.Text + "\n")
		}
	}
	var written []string
	for _, m := range turn {
		if m.Role == llm.RoleAssistant {
			for _, w := range productWrites(m) {
				written = append(written, w.path)
			}
		}
	}
	var out []string
next:
	for _, name := range namedSourceFiles(said.String()) {
		if isTestFile(name) {
			continue
		}
		n := strings.TrimPrefix(name, "./")
		for _, w := range written {
			w = strings.TrimPrefix(w, "./")
			if w == n || strings.HasSuffix(w, "/"+n) || strings.HasSuffix(n, "/"+w) {
				continue next
			}
		}
		out = append(out, name)
	}
	return out
}
