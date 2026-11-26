package agent

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strings"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// Prompt-cache prefixes only work while the request stays append-only. The
// cache ledger says a prefix was paid for twice; this says which message
// stopped matching, which is the part you can act on.
//
// Every request is fingerprinted per message. The next request is compared
// against it, and the first index where the two disagree is where the
// cache stopped being usable — a message edited in place, a system prompt
// carrying today's date, a tool list that grew mid-turn. A request that is
// a pure extension of the last one has no drift and is what should
// normally happen.

// drift is what changed between two consecutive requests.
type drift struct {
	Turn int
	// At is the index of the first message that differs, or -1 when the
	// new request only appended.
	At       int
	Was, Now int  // message counts
	System   bool // the system prompt changed
	Tools    bool // the tool list changed
	NameAt   string
}

// prefixWatch remembers the last request's shape.
type prefixWatch struct {
	msgs   []uint64
	system uint64
	tools  uint64
	drifts []drift
	// where is how the latest request diverged from the one before it
	// ("" when it only appended); the cache ledger reads it to name the
	// cause of a miss.
	where string
}

// note fingerprints a request and records how it differs from the last.
func (w *prefixWatch) note(turn int, system string, msgs []llm.Message, tools []llm.ToolDef) {
	cur := make([]uint64, len(msgs))
	for i, m := range msgs {
		cur[i] = hashMessage(m)
	}
	sys, tl := hashString(system), hashTools(tools)
	w.where = ""
	if w.msgs != nil {
		d := drift{Turn: turn, At: -1, Was: len(w.msgs), Now: len(cur), System: sys != w.system, Tools: tl != w.tools}
		for i := 0; i < len(cur) && i < len(w.msgs); i++ {
			if cur[i] != w.msgs[i] {
				d.At = i
				if i < len(msgs) {
					d.NameAt = describe(msgs[i])
				}
				break
			}
		}
		// Losing messages without any of the survivors changing is a
		// compaction, which is a rewrite too.
		if d.At == -1 && len(cur) < len(w.msgs) {
			d.At = len(cur)
			d.NameAt = "the history was shortened"
		}
		if d.At >= 0 || d.System || d.Tools {
			w.drifts = append(w.drifts, d)
			w.where = d.where(msgs)
		}
	}
	w.msgs, w.system, w.tools = cur, sys, tl
}

// where names the first point at which a request diverged from its
// predecessor, in the order the provider reads a prompt: system prompt,
// tool definitions, then messages.
func (d drift) where(msgs []llm.Message) string {
	switch {
	case d.System:
		return "system prompt changed"
	case d.Tools:
		return "tool definitions changed"
	case d.At >= 0 && d.At < len(msgs):
		return fmt.Sprintf("message %d (%s) changed", d.At+1, messageKind(msgs[d.At]))
	case d.At >= 0:
		return "history shortened"
	}
	return ""
}

// messageKind is a short name for what a message carries.
func messageKind(m llm.Message) string {
	var use, res bool
	for _, b := range m.Blocks {
		switch b.Type {
		case llm.BlockToolResult:
			res = true
		case llm.BlockToolUse:
			use = true
		}
	}
	switch {
	case res:
		return "tool result"
	case use:
		return "tool call"
	}
	return string(m.Role)
}

func hashMessage(m llm.Message) uint64 {
	h := sha256.New()
	h.Write([]byte(m.Role))
	for _, b := range m.Blocks {
		h.Write([]byte(b.Type))
		h.Write([]byte(b.ID))
		h.Write([]byte(b.Name))
		h.Write([]byte(b.Text))
		h.Write([]byte(b.ToolUseID))
		h.Write(b.Input)
	}
	return binary.BigEndian.Uint64(h.Sum(nil)[:8])
}

func hashString(s string) uint64 {
	sum := sha256.Sum256([]byte(s))
	return binary.BigEndian.Uint64(sum[:8])
}

func hashTools(defs []llm.ToolDef) uint64 {
	h := sha256.New()
	for _, d := range defs {
		h.Write([]byte(d.Name))
		h.Write([]byte(d.Description))
		h.Write(d.InputSchema)
	}
	return binary.BigEndian.Uint64(h.Sum(nil)[:8])
}

// describe names a message for a report.
func describe(m llm.Message) string {
	kinds := map[string]int{}
	for _, b := range m.Blocks {
		kinds[string(b.Type)]++
	}
	parts := make([]string, 0, len(kinds))
	for _, k := range []string{"text", "thinking", "tool_use", "tool_result", "image"} {
		if n := kinds[k]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, k))
		}
	}
	if len(parts) == 0 {
		return string(m.Role)
	}
	return string(m.Role) + " (" + strings.Join(parts, ", ") + ")"
}

// PrefixReport says where the cached prefix stopped matching, and how often.
func (a *Agent) PrefixReport() string {
	a.usageMu.Lock()
	w := a.prefix
	a.usageMu.Unlock()
	if len(w.msgs) == 0 {
		return "no request has been built yet"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "last request: %d message(s)\n", len(w.msgs))
	if len(w.drifts) == 0 {
		b.WriteString("\nevery request so far only appended to the one before it: the cached prefix held")
		return b.String()
	}
	fmt.Fprintf(&b, "\n%s where the prefix stopped matching:\n", textfmt.Plural(len(w.drifts), "point"))
	for i, d := range w.drifts {
		if i == 8 {
			fmt.Fprintf(&b, "  … and %d more\n", len(w.drifts)-8)
			break
		}
		switch {
		case d.At >= 0:
			fmt.Fprintf(&b, "  turn %-3d message %d of %d changed: %s\n", d.Turn, d.At+1, d.Was, d.NameAt)
		case d.System:
			fmt.Fprintf(&b, "  turn %-3d the system prompt changed\n", d.Turn)
		default:
			fmt.Fprintf(&b, "  turn %-3d the tool list changed\n", d.Turn)
		}
		if d.At >= 0 && d.System {
			fmt.Fprintf(&b, "           (the system prompt changed too)\n")
		}
		if d.At >= 0 && d.Tools {
			fmt.Fprintf(&b, "           (the tool list changed too)\n")
		}
	}
	b.WriteString("\nEverything from that message onward is billed as fresh input.")
	return strings.TrimRight(b.String(), "\n")
}
