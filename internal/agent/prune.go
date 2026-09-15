package agent

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/textfmt"
	"github.com/xmonader/janetcode/internal/tool"
)

// Prune applies lossless-in-practice tiers to the history before a
// summary is paid for: earlier results of read-only calls repeated with
// identical input are stubbed out, and old tool outputs are trimmed to
// their head and tail. The recent tail is untouched. It returns the
// number of bytes removed. Pruning is only done here, on the way to
// compaction, because rewriting old messages invalidates the cached
// request prefix.
func (a *Agent) Prune() int {
	n := len(a.Messages)
	tail := tailStart(a.Messages)
	// Latest occurrence of every read-only call, by tool and canonical input.
	latest := map[string]string{} // key → tool_use id
	readOnly := map[string]bool{}
	for _, m := range a.Messages {
		for _, b := range m.Blocks {
			if b.Type != llm.BlockToolUse {
				continue
			}
			ro, seen := readOnly[b.Name]
			if !seen {
				t, ok := a.Tools.Get(b.Name)
				ro = ok && !t.Mutating()
				readOnly[b.Name] = ro
			}
			if ro {
				latest[b.Name+"\x00"+canonical(b.Input)] = b.ID
			}
		}
	}
	keep := map[string]bool{}
	for _, id := range latest {
		keep[id] = true
	}
	useKey := map[string]string{}
	label := map[string][2]string{} // tool_use id → name, subject
	for _, m := range a.Messages {
		for _, b := range m.Blocks {
			if b.Type != llm.BlockToolUse {
				continue
			}
			label[b.ID] = [2]string{b.Name, subjectOf(b.Name, b.Input)}
			if readOnly[b.Name] {
				useKey[b.ID] = b.Name + "\x00" + canonical(b.Input)
			}
		}
	}
	saved := 0
	for i := 0; i < tail && i < n; i++ {
		for j, b := range a.Messages[i].Blocks {
			if b.Type != llm.BlockToolResult {
				continue
			}
			before := len(b.Text)
			switch {
			case useKey[b.ToolUseID] != "" && !keep[b.ToolUseID] && len(b.Text) > len(superseded):
				a.setBlockText(i, j, superseded)
			case len(b.Text) > pruneKeep:
				a.setBlockText(i, j, a.trimOld(b.ToolUseID, label[b.ToolUseID][0], label[b.ToolUseID][1], b.Text))
			}
			saved += before - len(a.Messages[i].Blocks[j].Text)
		}
	}
	if saved > 0 {
		a.contextManager.rewritten()
	}
	return saved
}

// pruneTo replaces old tool results with one-line placeholders until the
// estimate is at or under target tokens, or the recent tail is reached.
// Results go in attention order, least attended first: a result the model
// never referred to again (a grep it moved on from) goes before a file it
// kept editing, and a command that was re-run later goes first of all
// since its output is stale. Full outputs go to the archive first. It
// returns how many results were replaced.
func (a *Agent) pruneTo(target int) int {
	if target <= 0 || a.EstimateTokens() <= target {
		return 0
	}
	tail := tailStart(a.Messages)
	uses := map[string]llm.Block{}
	for _, m := range a.Messages[:min(tail, len(a.Messages))] {
		for _, b := range m.Blocks {
			if b.Type == llm.BlockToolUse {
				uses[b.ID] = b
			}
		}
	}
	type candidate struct {
		i, j    int
		use     llm.Block
		lastRef int
	}
	var cands []candidate
	for i := 0; i < tail && i < len(a.Messages); i++ {
		for j, b := range a.Messages[i].Blocks {
			if b.Type != llm.BlockToolResult || strings.HasPrefix(b.Text, prunedPrefix) || b.Text == superseded {
				continue
			}
			u, ok := uses[b.ToolUseID]
			if !ok {
				continue
			}
			cands = append(cands, candidate{i: i, j: j, use: u, lastRef: a.lastReference(i, u)})
		}
	}
	sort.SliceStable(cands, func(x, y int) bool {
		if cands[x].lastRef != cands[y].lastRef {
			return cands[x].lastRef < cands[y].lastRef
		}
		return cands[x].i < cands[y].i
	})
	replaced := 0
	for _, c := range cands {
		b := a.Messages[c.i].Blocks[c.j]
		id := ""
		if a.Archive != nil {
			if id = a.Archive.IDFor(b.ToolUseID); id == "" {
				id, _ = a.Archive.PutSubject(b.ToolUseID, c.use.Name, subjectOf(c.use.Name, c.use.Input), b.Text)
			}
		}
		ph := placeholder(c.use.Name, c.use.Input, b.Text, id)
		if len(ph) >= len(b.Text) {
			continue
		}
		a.setBlockText(c.i, c.j, ph)
		replaced++
		a.contextManager.rewritten()
		if a.EstimateTokens() <= target {
			return replaced
		}
	}
	return replaced
}

// lastReference is the index of the last message after at (the result's
// position) whose assistant text or tool inputs mention what the call at
// `use` was about: its path, pattern or command. A result nothing came
// back to keeps its own index; a bash command repeated verbatim later
// gets -1 because the earlier output is stale.
func (a *Agent) lastReference(at int, use llm.Block) int {
	subjects := subjectsOf(use.Name, use.Input)
	if len(subjects) == 0 {
		return at
	}
	last := at
	for i := len(a.Messages) - 1; i > at; i-- {
		m := a.Messages[i]
		for _, b := range m.Blocks {
			var text string
			switch b.Type {
			case llm.BlockToolUse:
				if use.Name == "bash" && b.Name == "bash" && canonical(b.Input) == canonical(use.Input) {
					return -1
				}
				text = string(b.Input)
			case llm.BlockText:
				if m.Role != llm.RoleAssistant {
					continue
				}
				text = b.Text
			default:
				continue
			}
			for _, s := range subjects {
				if strings.Contains(text, s) {
					return i
				}
			}
		}
	}
	return last
}

// subjectOf is the first subject of a call, or "": the label an archive
// entry carries.
func subjectOf(name string, input json.RawMessage) string {
	if s := subjectsOf(name, input); len(s) > 0 {
		return s[0]
	}
	return ""
}

// subjectsOf names what a tool call was about, for reference matching.
func subjectsOf(name string, input json.RawMessage) []string {
	var in map[string]any
	json.Unmarshal(input, &in)
	var out []string
	for _, k := range []string{"path", "pattern", "command", "symbol", "query", "url"} {
		v, _ := in[k].(string)
		v = strings.TrimSpace(v)
		if len(v) < 3 || v == "." {
			continue
		}
		if k == "path" {
			v = strings.TrimSuffix(v, "/")
		}
		out = append(out, v)
	}
	return out
}

// canonical normalises tool input JSON so formatting differences do not
// defeat deduplication.
func canonical(raw []byte) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return string(raw)
	}
	out, err := json.Marshal(v)
	if err != nil {
		return string(raw)
	}
	return string(out)
}

// trimOld shortens an old tool result to pruneKeep bytes. The full text is
// archived first (unless it already was) so the model can get it back.
func (a *Agent) trimOld(toolUse, name, subject, text string) string {
	if a.Archive == nil {
		return textfmt.TruncateTo(text, pruneKeep)
	}
	if id := a.Archive.IDFor(toolUse); id != "" {
		// Already archived: keep the note that names the id, trim the rest.
		if i := strings.Index(text, "\n"); i > 0 && strings.HasPrefix(text, "[output is ") {
			return text[:i+1] + textfmt.TruncateTo(text[i+1:], pruneKeep)
		}
		return archiveNote(id, len(text), pruneKeep, outputDigest(text)) + tool.EscapeDelimiters(textfmt.TruncateTo(text, pruneKeep))
	}
	id, err := a.Archive.PutSubject(toolUse, name, subject, text)
	if err != nil {
		return textfmt.TruncateTo(text, pruneKeep)
	}
	return archiveNote(id, len(text), pruneKeep, outputDigest(text)) + tool.EscapeDelimiters(textfmt.TruncateTo(text, pruneKeep))
}
