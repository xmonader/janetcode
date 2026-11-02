package agent

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/tool"
)

// A read_file whose slice is identical to one already returned, whose
// file has not been edited since, and whose earlier text is still
// verbatim in the history, is answered with a short pointer instead of
// the text again; and the stand-ins of one step (these pointers and the
// in-batch "deduped" answers) beyond standInsKept are dropped from the
// recorded step. In hero_agent one MiMo step issued 240 read_file calls
// over 68 distinct slices of one spec (~810 KB of results from one step)
// and another 1,056 calls whose 254-byte stand-ins still made the next
// request 575 KB; the re-read rule and the fold are its fixes (#884,
// #908). The read still runs (it is cheap; the context is what costs),
// so a changed file always shows its new text. The pointer is not given
// twice running: reading the same slice straight after a pointer
// returns the text, which stands in for hero_agent's force flag. The
// earlier copy stops being live when compaction or pruning rewrote it
// (this is checked against the history, not remembered), and any
// mutating call on the file forgets its views. prune.go does the same
// for what the history already holds at compaction time.
// JANETCODE_OFF=readdedup turns it off.

// standInsKept is how many stand-ins of one step keep their call and
// result; the rest are removed from the step (hero_agent: 4).
const standInsKept = 4

// readView is one read_file slice returned earlier this session.
type readView struct {
	callID  string
	path    string
	sum     [32]byte
	pointed bool // the last re-read already got a pointer
}

// readViews is the session's record of returned read_file slices.
type readViews struct {
	byKey map[string]*readView
}

const repeatPrefix = "[repeat of call "

func unchangedResult(callID, slice string) string {
	return fmt.Sprintf("%s%s: %s is unchanged since that result, which is still in your context above. "+
		"Use it. If you need the text again anyway, read the same slice once more.]", repeatPrefix, callID, slice)
}

// isStandIn reports whether a tool result is one of the short answers
// that point at another result.
func isStandIn(text string) bool {
	return text == dedupedResult || strings.HasPrefix(text, repeatPrefix)
}

// liveResult reports whether the history still holds the result of call
// id with exactly this text.
func liveResult(msgs []llm.Message, id, text string) bool {
	for i := len(msgs) - 1; i >= 0; i-- {
		for _, b := range msgs[i].Blocks {
			if b.Type == llm.BlockToolResult && b.ToolUseID == id {
				return b.Text == text
			}
		}
	}
	return false
}

// readSlice returns the path and canonical key of a read_file call.
func readSlice(u llm.Block) (path, key string, ok bool) {
	if u.Name != "read_file" {
		return "", "", false
	}
	var in struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if json.Unmarshal(u.Input, &in) != nil || in.Path == "" {
		return "", "", false
	}
	return in.Path, canonical(u.Input), true
}

func sliceLabel(u llm.Block) string {
	var in struct {
		Path          string `json:"path"`
		Offset, Limit int
	}
	_ = json.Unmarshal(u.Input, &in)
	switch {
	case in.Offset > 0 && in.Limit > 0:
		return fmt.Sprintf("%s (from line %d, %d lines)", in.Path, in.Offset, in.Limit)
	case in.Offset > 0:
		return fmt.Sprintf("%s (from line %d)", in.Path, in.Offset)
	}
	return in.Path
}

// dedupReads rewrites, in place, the results of this step that re-read an
// unchanged live slice, then records the slices the step returned and
// forgets those it changed. msgs is the history before the step's
// results. It returns how many results it replaced.
func (a *Agent) dedupReads(msgs []llm.Message, uses []llm.Block, results []llm.Block) int {
	if a.Off["readdedup"] {
		return 0
	}
	v := &a.pl.reads
	if v.byKey == nil {
		v.byKey = map[string]*readView{}
	}
	replaced := 0
	for i, u := range uses {
		if i >= len(results) {
			break
		}
		if t, ok := a.Tools.Get(u.Name); ok && tool.MutatingCall(t, u.Input) {
			v.forget(a, t, u)
			continue
		}
		path, key, ok := readSlice(u)
		if !ok || results[i].IsError || isStandIn(results[i].Text) {
			continue
		}
		sum := sha256.Sum256([]byte(results[i].Text))
		old := v.byKey[key]
		if old != nil && old.sum == sum && !old.pointed && liveResult(msgs, old.callID, results[i].Text) {
			results[i].Text = unchangedResult(old.callID, sliceLabel(u))
			old.pointed = true
			replaced++
			continue
		}
		v.byKey[key] = &readView{callID: replyID(u), path: path, sum: sum}
	}
	return replaced
}

// forget drops the views of every file a mutating call may have changed:
// the paths it names, or all of them when it names none (a shell).
func (v *readViews) forget(a *Agent, t tool.Tool, u llm.Block) {
	var paths []string
	if tc, ok := t.(tool.Toucher); ok {
		paths, _ = tc.Touches(u.Input)
	}
	if len(paths) == 0 {
		clear(v.byKey)
		return
	}
	for k, rv := range v.byKey {
		for _, p := range paths {
			if sameFile(rv.path, p) {
				delete(v.byKey, k)
				break
			}
		}
	}
}

func sameFile(x, y string) bool {
	return x == y || strings.HasSuffix(y, "/"+strings.TrimPrefix(x, "./")) || strings.HasSuffix(x, "/"+strings.TrimPrefix(y, "./"))
}

// foldStandIns names the stand-ins of the step past the first keep, and
// the line that goes on the last kept one. dropped is nil when the step
// has no more than keep.
func foldStandIns(uses, results []llm.Block, keep int) (dropped map[string]bool, note string) {
	var ids []string
	for i, r := range results {
		if i < len(uses) && isStandIn(r.Text) {
			ids = append(ids, replyID(uses[i]))
		}
	}
	if len(ids) <= keep {
		return nil, ""
	}
	dropped = map[string]bool{}
	for _, id := range ids[keep:] {
		dropped[id] = true
	}
	return dropped, fmt.Sprintf("\n[%d more repeated calls in this step were dropped from the history.]", len(ids)-keep)
}

// withoutDropped removes the dropped results. The note goes on the last
// stand-in that stays, and text the step appended to its last result
// (extra) moves to the new last result if that one was dropped.
func withoutDropped(results []llm.Block, dropped map[string]bool, note, extra string) []llm.Block {
	var out []llm.Block
	lastStand := -1
	for _, r := range results {
		if dropped[r.ToolUseID] {
			continue
		}
		if isStandIn(r.Text) {
			lastStand = len(out)
		}
		out = append(out, r)
	}
	if lastStand >= 0 {
		out[lastStand].Text += note
	}
	if n := len(results); n > 0 && dropped[results[n-1].ToolUseID] && extra != "" && len(out) > 0 {
		out[len(out)-1].Text += extra
	}
	return out
}

// dropCalls removes the dropped tool_use blocks from the last assistant
// message (a copy of its blocks: the slice may be shared).
func (a *Agent) dropCalls(dropped map[string]bool) {
	if len(dropped) == 0 || len(a.Messages) == 0 {
		return
	}
	m := &a.Messages[len(a.Messages)-1]
	var blocks []llm.Block
	for _, b := range m.Blocks {
		if b.Type == llm.BlockToolUse && dropped[replyID(b)] {
			continue
		}
		blocks = append(blocks, b)
	}
	m.Blocks = blocks
}
