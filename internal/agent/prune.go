package agent

import (
	"encoding/json"
	"fmt"
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
	calls := answeredCalls(a.Messages)
	saved := 0
	for i := 0; i < tail && i < n; i++ {
		for j, b := range a.Messages[i].Blocks {
			if b.Type != llm.BlockToolResult {
				continue
			}
			id := b.ToolUseID
			if u, ok := calls[[2]int{i, j}]; ok {
				id = u.ID
			}
			before := len(b.Text)
			switch {
			case useKey[id] != "" && !keep[id] && len(b.Text) > len(superseded):
				a.setBlockText(i, j, superseded)
			case len(afterArchiveNote(b.Text)) > pruneKeep+pruneSlack:
				a.setBlockText(i, j, a.trimOld(id, label[id][0], label[id][1], b.Text))
			}
			saved += before - len(a.Messages[i].Blocks[j].Text)
		}
	}
	// The arguments of old calls are resent too: every earlier write_file
	// body and edit payload, until a summary drops them. The file on disk
	// is what counts now, so their long strings go; the keys and short
	// values (the path) stay, and the input is still the JSON object
	// every provider requires.
	for i := 0; i < tail && i < n; i++ {
		for j, b := range a.Messages[i].Blocks {
			if b.Type != llm.BlockToolUse || len(b.Input) <= pruneInputKeep {
				continue
			}
			if short, ok := shrinkInput(b.Input); ok {
				saved += len(b.Input) - len(short)
				a.setBlockInput(i, j, short)
			}
		}
	}
	if saved > 0 {
		a.contextManager.rewritten()
	}
	return saved
}

// pruneSlack is how far past pruneKeep a result may run and still count
// as trimmed: TruncateTo keeps pruneKeep bytes and adds its own omission
// marker, and escaping the harness's delimiters can grow the preview a
// little. Without it every later prune found each trimmed result a few
// bytes over, cut it again — nesting a second marker inside the first —
// and so rewrote old messages the cached prefix still held, and reported
// the pass as having saved something.
const pruneSlack = 128

// afterArchiveNote is a tool result without the one-line archive note a
// stash or an earlier trim put at its head: the part trimOld cuts.
func afterArchiveNote(text string) string {
	if i := strings.Index(text, "\n"); i > 0 && strings.HasPrefix(text, "[output is ") {
		return text[i+1:]
	}
	return text
}

// pruneInputKeep is the size of a tool call's arguments above which an
// old call's long strings are omitted, and pruneStringKeep the length of
// one string value that is kept whole.
const (
	pruneInputKeep  = 2048
	pruneStringKeep = 256
)

// shrinkInput replaces every string value longer than pruneStringKeep in a
// call's arguments with a note of its length. ok is false when nothing
// was shorter for it.
func shrinkInput(input json.RawMessage) (json.RawMessage, bool) {
	var v any
	if json.Unmarshal(input, &v) != nil {
		return nil, false
	}
	var shrink func(any) any
	shrink = func(v any) any {
		switch x := v.(type) {
		case string:
			if len(x) > pruneStringKeep {
				return fmt.Sprintf("[%d characters omitted from an earlier call; read the file for what is there now]", len(x))
			}
		case map[string]any:
			for k, e := range x {
				x[k] = shrink(e)
			}
		case []any:
			for k, e := range x {
				x[k] = shrink(e)
			}
		}
		return v
	}
	out, err := json.Marshal(shrink(v))
	if err != nil || len(out) >= len(input) {
		return nil, false
	}
	return out, true
}

// answeredCalls pairs each tool result, by message and block index, with
// the tool_use it answers. A result carries the id the call went out
// under, which is the server's own when the harness renamed the call
// (see callids.go) — and a server that numbers its calls per response
// sends call_0 every time, so that id names many calls. The call a result
// answers is in the assistant message just before it, so results are
// matched there, each call taken once and in order.
func answeredCalls(msgs []llm.Message) map[[2]int]llm.Block {
	out := map[[2]int]llm.Block{}
	for i := 1; i < len(msgs); i++ {
		if msgs[i-1].Role != llm.RoleAssistant {
			continue
		}
		uses := msgs[i-1].ToolUses()
		taken := make([]bool, len(uses))
		for j, b := range msgs[i].Blocks {
			if b.Type != llm.BlockToolResult {
				continue
			}
			for k, u := range uses {
				if !taken[k] && (u.WireID() == b.ToolUseID || u.ID == b.ToolUseID) {
					taken[k] = true
					out[[2]int{i, j}] = u
					break
				}
			}
		}
	}
	return out
}

// pruneTo replaces old tool results with one-line placeholders until the
// estimate is at or under target tokens, or the recent tail is reached.
// Results go in attention order, least attended first: a result the model
// never referred to again (a grep it moved on from) goes before a file it
// kept editing, and a command that was re-run later goes first of all
// since its output is stale. Full outputs go to the archive first. It
// returns how many results were replaced.
func (a *Agent) pruneTo(target int) int {
	if target <= 0 {
		return 0
	}
	// The estimate is carried down by hand rather than recomputed after
	// each replacement. EstimateTokens walks the whole history and
	// re-measures the system prompt, and rewritten() resets the "already
	// billed" mark, so the loop used to be quadratic in a history of
	// exactly the size that gets here. Each replacement changes one block
	// of text and nothing else, so the difference is all there is.
	est := a.EstimateTokens()
	if est <= target {
		return 0
	}
	tail := tailStart(a.Messages)
	calls := answeredCalls(a.Messages)
	// A result that is not right after its call (a history an older build
	// wrote) is still found by id.
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
			u, ok := calls[[2]int{i, j}]
			if !ok {
				if u, ok = uses[b.ToolUseID]; !ok {
					continue
				}
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
			if id = archiveNoteID(b.Text); id == "" {
				id, _ = a.Archive.PutSubject(c.use.ID, c.use.Name, subjectOf(c.use.Name, c.use.Input), b.Text)
			}
		}
		ph := placeholder(c.use.Name, c.use.Input, b.Text, id)
		if len(ph) >= len(b.Text) {
			continue
		}
		a.setBlockText(c.i, c.j, ph)
		replaced++
		a.contextManager.rewritten()
		est -= estimateTokens(b.Text) - estimateTokens(ph)
		if est <= target {
			break
		}
	}
	if replaced > 0 {
		a.EstimateTokens() // republish the figure the status bar reads
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
	if archiveNoteID(text) != "" {
		// Already archived: keep the note that names the id, trim the rest.
		if i := strings.Index(text, "\n"); i > 0 {
			return text[:i+1] + textfmt.TruncateTo(text[i+1:], pruneKeep)
		}
		return text
	}
	id, err := a.Archive.PutSubject(toolUse, name, subject, text)
	if err != nil {
		return textfmt.TruncateTo(text, pruneKeep)
	}
	return archiveNote(id, len(text), pruneKeep, outputDigest(text)) + tool.EscapeDelimiters(textfmt.TruncateTo(text, pruneKeep))
}
