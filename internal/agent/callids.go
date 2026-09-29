package agent

import (
	"fmt"

	"github.com/xmonader/janetcode/internal/llm"
)

// Tool-call ids come from the model's server and are not always usable
// as identity. Some servers number calls per response, so the second
// turn reuses call_0; some omit the id on a call they streamed as text;
// some repeat an id inside one response. Everything the harness keys by
// call id — the transcript entry a result lands in, the permission
// prompt, the event log, the archive — then attaches two different calls
// to one row.
//
// So the harness gives every call an id of its own and keeps the
// server's alongside it. The reply still goes back under the id the
// server sent, because that is the only id it knows.

// normalizeCallIDs gives each tool call in a message an id unique within
// the conversation, remembering the original for the reply. used is the
// set of ids already handed out, and is added to.
func normalizeCallIDs(m *llm.Message, used map[string]bool, turn, step int) {
	for i := range m.Blocks {
		b := &m.Blocks[i]
		if b.Type != llm.BlockToolUse {
			continue
		}
		raw := b.ID
		switch {
		case raw == "":
			// Nothing to reply under; a synthesised id is at least
			// unique, and a server that sends no id does not match on
			// one either.
			b.ID = uniqueID(fmt.Sprintf("call_%d_%d_%d", turn, step, i+1), used)
		case used[raw]:
			b.ReplyID = raw
			b.ID = uniqueID(raw, used)
		default:
			b.ID = raw
		}
		used[b.ID] = true
	}
}

// noteCallIDs adds every tool-call id in msgs to used. A conversation
// that arrives whole — a resumed session, one opened with /resume, a
// forked child's inherited history — already holds ids the server handed
// out, and a server that numbers calls per response will send the same
// call_0 again; without them in the set the new call would share an id
// with the old one.
func noteCallIDs(msgs []llm.Message, used map[string]bool) {
	for _, m := range msgs {
		for _, b := range m.Blocks {
			if b.Type == llm.BlockToolUse && b.ID != "" {
				used[b.ID] = true
			}
		}
	}
}

func uniqueID(base string, used map[string]bool) string {
	if !used[base] {
		return base
	}
	for n := 2; ; n++ {
		candidate := fmt.Sprintf("%s#%d", base, n)
		if !used[candidate] {
			return candidate
		}
	}
}

// replyID is the id a tool result must carry: the server's own when the
// harness had to rename the call, else the call's id.
func replyID(b llm.Block) string {
	if b.ReplyID != "" {
		return b.ReplyID
	}
	return b.ID
}
