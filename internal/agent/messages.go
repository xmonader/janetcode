package agent

import (
	"strings"

	"github.com/xmonader/janetcode/internal/llm"
)

// appendMessage, appendToLast, setMessages and setBlockText are the only
// ways Messages changes. The turn goroutine is the only writer and reads
// it freely; they take msgMu so a reader on another goroutine — a
// subagent transcript view, /btw — can take a consistent copy with
// MessagesSnapshot while a turn runs.
func (a *Agent) appendMessage(m llm.Message) {
	a.msgMu.Lock()
	a.Messages = append(a.Messages, m)
	a.msgMu.Unlock()
}

func (a *Agent) appendToLast(blocks ...llm.Block) {
	a.msgMu.Lock()
	n := len(a.Messages)
	a.Messages[n-1].Blocks = append(a.Messages[n-1].Blocks, blocks...)
	a.msgMu.Unlock()
}

func (a *Agent) setMessages(msgs []llm.Message) {
	a.msgMu.Lock()
	a.Messages = msgs
	a.msgMu.Unlock()
}

func (a *Agent) setBlockText(i, j int, text string) {
	a.msgMu.Lock()
	a.Messages[i].Blocks[j].Text = text
	a.msgMu.Unlock()
}

// MessagesSnapshot is a copy of the conversation, safe to take from any
// goroutine while a turn runs. Each message's blocks are copied too, since
// pruning rewrites a block's text in place.
func (a *Agent) MessagesSnapshot() []llm.Message {
	a.msgMu.Lock()
	defer a.msgMu.Unlock()
	out := make([]llm.Message, len(a.Messages))
	for i, m := range a.Messages {
		m.Blocks = append([]llm.Block(nil), m.Blocks...)
		out[i] = m
	}
	return out
}

// LastAssistantText is the last assistant reply, for /copy. Under msgMu
// like every other off-turn reader: /copy is not idle-only, so the turn
// goroutine may be appending while this walks backwards.
func (a *Agent) LastAssistantText() string {
	if a == nil {
		return ""
	}
	a.msgMu.Lock()
	defer a.msgMu.Unlock()
	for i := len(a.Messages) - 1; i >= 0; i-- {
		if a.Messages[i].Role == llm.RoleAssistant {
			if t := strings.TrimSpace(a.Messages[i].Text()); t != "" {
				return t
			}
		}
	}
	return ""
}

// repeatMinBytes is the smallest attachment worth checking for. Below it
// the comparison costs more than the bytes it could save, and a short
// repeated line may be meant to be repeated.
const repeatMinBytes = 512

// dropRepeatedAttachments removes any block the Prepare hook added whose
// text is already in the history word for word. The repository map is
// re-ranked and re-attached on every user turn, and on a measured
// ten-turn session three of the ten came out byte-identical to a map
// already above them — six kilobytes each, paid for again to say what the
// context already said. Skills have their own six-turn window, which
// re-attaches an identical block on the seventh whether or not the first
// is still there.
//
// Comparing against the live history rather than remembering what was
// sent is what makes this safe: once compaction takes the old copy out,
// the match fails and the attachment comes back.
//
// own is how many blocks the turn itself brought; only what Prepare added
// past that is considered, so nothing the user wrote is ever dropped.
func (a *Agent) dropRepeatedAttachments(blocks []llm.Block, own int) ([]llm.Block, int) {
	if len(blocks) <= own {
		return blocks, 0
	}
	repeat := func(b llm.Block) bool {
		return b.Type == llm.BlockText && len(b.Text) >= repeatMinBytes && a.historyHasText(b.Text)
	}
	saved := 0
	for _, b := range blocks[own:] {
		if repeat(b) {
			saved += len(b.Text)
		}
	}
	if saved == 0 {
		return blocks, 0
	}
	kept := blocks[:own:own]
	for _, b := range blocks[own:] {
		if !repeat(b) {
			kept = append(kept, b)
		}
	}
	return kept, saved
}

// historyHasText reports whether some text block of the current history
// is exactly s. Lengths are compared first, so the byte comparison runs
// only for a real candidate.
func (a *Agent) historyHasText(s string) bool {
	for _, m := range a.Messages {
		for _, b := range m.Blocks {
			if b.Type == llm.BlockText && len(b.Text) == len(s) && b.Text == s {
				return true
			}
		}
	}
	return false
}
