package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/xmonader/janetcode/internal/llm"
)

// TypeRequest records one model call as the model was shown it. The
// model-visible-means-logged rule: everything a request carried is either
// in the session already (the messages, as surface events) or in this
// event (the system prompt with its memory, skills, pins and modes, and
// the tool schemas), so a replay can rebuild the request offline and
// compare it with what was sent. Log-only: the payload is Extra, a
// ModelRequest.
const TypeRequest = "model/request"

// ModelRequest is the Extra of a TypeRequest event. The three digests are
// always present. System and Tools are written in full only when they
// differ from the previous request on the same log, so a long session
// carries them a handful of times; a reader carries the last one forward.
type ModelRequest struct {
	Model       string `json:"model"`
	Provider    string `json:"provider,omitempty"`
	MaxTokens   int    `json:"max_tokens,omitempty"`
	Effort      string `json:"effort,omitempty"`
	SystemSHA   string `json:"system_sha"`
	ToolsSHA    string `json:"tools_sha"`
	MessagesSHA string `json:"messages_sha"`
	// Messages is how many messages the request carried.
	Messages int `json:"messages"`
	// SentSHA is the digest of the messages as the provider received
	// them, written only when it differs from MessagesSHA: image blocks
	// bridged to text for a model that cannot read them. Those bytes are
	// not reconstructable from the log, only vouched for.
	SentSHA string        `json:"sent_sha,omitempty"`
	System  string        `json:"system,omitempty"`
	Tools   []llm.ToolDef `json:"tools,omitempty"`
}

// DigestText is the sha256 a request event names a system prompt by.
func DigestText(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// DigestJSON is the sha256 of v's JSON encoding; it names a message list
// or a tool list. A value that does not encode digests as "unencodable"
// rather than colliding with an empty one.
func DigestJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "unencodable"
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// DigestMessages names a message list the way a request event does; a nil
// and an empty list digest alike.
func DigestMessages(msgs []llm.Message) string {
	if len(msgs) == 0 {
		return DigestJSON([]llm.Message{})
	}
	return DigestJSON(msgs)
}

// DigestTools names a tool list the way a request event does.
func DigestTools(defs []llm.ToolDef) string {
	if len(defs) == 0 {
		return DigestJSON([]llm.ToolDef{})
	}
	return DigestJSON(defs)
}

// Event wraps r as the log-only event that records it.
func (r ModelRequest) Event() Event {
	b, _ := json.Marshal(r)
	return Event{Type: TypeRequest, Extra: b}
}

// RequestOf decodes a TypeRequest event.
func RequestOf(e Event) (ModelRequest, error) {
	var r ModelRequest
	err := json.Unmarshal(e.Extra, &r)
	return r, err
}
