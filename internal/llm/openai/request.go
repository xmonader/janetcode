package openai

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/llm"
)

// trailingNewlines counts s's trailing newlines, up to two.
func trailingNewlines(s string) int {
	n := 0
	for n < 2 && strings.HasSuffix(s[:len(s)-n], "\n") {
		n++
	}
	return n
}

func buildRequest(req llm.Request) (wireRequest, error) {
	if err := req.Validate(); err != nil {
		return wireRequest{}, err
	}
	w := wireRequest{Model: req.Model, MaxTokens: req.MaxTokens, Stream: true}
	if req.Schema != nil && len(req.Schema.JSON) > 0 {
		w.ResponseFormat = &wireResponseFormat{
			Type:       "json_schema",
			JSONSchema: &wireJSONSchema{Name: req.Schema.Name, Schema: req.Schema.JSON, Strict: req.Schema.Strict},
		}
	}
	w.StreamOptions = &struct {
		IncludeUsage bool `json:"include_usage"`
	}{true}
	if req.System != "" {
		w.Messages = append(w.Messages, wireMessage{Role: "system", Content: req.System})
	}
	asstLeft := 0 // assistant messages not yet written, this one included
	for _, m := range req.Messages {
		if m.Role == llm.RoleAssistant {
			asstLeft++
		}
	}
	for _, m := range req.Messages {
		switch m.Role {
		case llm.RoleUser:
			// Tool results and plain text can both sit in a user turn; emit
			// tool results as "tool" messages and text as a "user" message.
			var (
				text   strings.Builder
				images []wirePart
			)
			for _, b := range m.Blocks {
				switch b.Type {
				case llm.BlockToolResult:
					content := b.Text
					if b.IsError {
						content = "Error: " + content
					}
					w.Messages = append(w.Messages, wireMessage{Role: "tool", ToolCallID: b.ToolUseID, Content: content})
				case llm.BlockText:
					// Each block is its own paragraph: a harness note and the
					// prompt typed after it must not run together.
					if text.Len() > 0 && b.Text != "" {
						text.WriteString(strings.Repeat("\n", 2-trailingNewlines(text.String())))
					}
					text.WriteString(b.Text)
				case llm.BlockImage:
					part := wirePart{Type: "image_url"}
					part.ImageURL = &struct {
						URL string `json:"url"`
					}{"data:" + b.MediaType + ";base64," + b.Data}
					images = append(images, part)
				}
			}
			switch {
			case len(images) > 0:
				parts := []wirePart{}
				if text.Len() > 0 {
					parts = append(parts, wirePart{Type: "text", Text: text.String()})
				}
				w.Messages = append(w.Messages, wireMessage{Role: "user", Content: append(parts, images...)})
			case text.Len() > 0:
				w.Messages = append(w.Messages, wireMessage{Role: "user", Content: text.String()})
			}
		case llm.RoleAssistant:
			am := wireMessage{Role: "assistant"}
			// With ReasoningLast set the cut moves each call: the message
			// that just aged out loses its reasoning, so the prompt cache
			// is broken from the oldest still-replayed message onward.
			// Only that one message changes between requests, which keeps
			// the cost small.
			dropThink := req.DropReasoning || (req.ReasoningLast > 0 && asstLeft > req.ReasoningLast)
			asstLeft--
			var text, think strings.Builder
			via := ""
			for _, b := range m.Blocks {
				switch b.Type {
				case llm.BlockThinking:
					if dropThink || b.Via == "" || b.Text == "" || (via != "" && b.Via != via) {
						continue
					}
					via = b.Via
					think.WriteString(b.Text)
				case llm.BlockText:
					text.WriteString(b.Text)
				case llm.BlockToolUse:
					tc := wireToolCall{ID: b.WireID(), Type: "function"}
					tc.Function.Name = b.Name
					tc.Function.Arguments = string(b.Input)
					if tc.Function.Arguments == "" {
						tc.Function.Arguments = "{}"
					}
					am.ToolCalls = append(am.ToolCalls, tc)
				}
			}
			am.Content = text.String()
			thought := tailChars(think.String(), req.ReasoningChars)
			switch via {
			case "reasoning_content":
				am.ReasoningContent = thought
			case "reasoning":
				am.Reasoning = thought
			}
			if text.Len() == 0 && len(am.ToolCalls) == 0 {
				// A reply that was only reasoning or nothing at all.
				// Kimi and other servers refuse an assistant message with
				// empty content and no tool calls ("must not be empty"),
				// which ended a run after a plan-budget cut.
				am.Content = emptyAssistant
			}
			w.Messages = append(w.Messages, am)
		default:
			return wireRequest{}, fmt.Errorf("openai: unsupported role %q", m.Role)
		}
	}
	for _, t := range req.Tools {
		var wt wireTool
		wt.Type = "function"
		wt.Function.Name = t.Name
		wt.Function.Description = t.Description
		wt.Function.Parameters = t.InputSchema
		if len(wt.Function.Parameters) == 0 {
			wt.Function.Parameters = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		w.Tools = append(w.Tools, wt)
	}
	return w, nil
}

// emptyAssistant stands in for an assistant turn with nothing to send.
const emptyAssistant = "(no reply)"

// shardsPromptCache is true for endpoints that route to a prompt-cache
// shard by hashing the prefix together with prompt_cache_key. Sending the
// field elsewhere risks a 400 mid-session for no benefit, which is worse
// than the cache miss it would avoid.
func shardsPromptCache(base string) bool {
	u := strings.ToLower(base)
	return strings.Contains(u, "api.openai.com") || strings.Contains(u, "openrouter.ai")
}

// defaultCacheKey is stable for the life of the process, which is the life
// of a session. It only has to be the same from one turn to the next; it
// is not a secret and identifies nothing.
func defaultCacheKey() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("janetcode-%d", time.Now().UnixNano())
	}
	return "janetcode-" + hex.EncodeToString(b[:])
}

// cacheableURL is true for endpoints that honour Anthropic-style
// cache_control breakpoints (and, for DashScope, need an opt-in header).
func cacheableURL(base string) bool {
	u := strings.ToLower(base)
	return strings.Contains(u, "dashscope") || strings.Contains(u, "openrouter.ai")
}

func extraHeaders(baseURL string) map[string]string {
	if strings.Contains(strings.ToLower(baseURL), "dashscope") {
		return map[string]string{"X-DashScope-CacheControl": "enable"}
	}
	return nil
}

func normalizeContent(content any) []wirePart {
	switch c := content.(type) {
	case string:
		if c == "" {
			return nil
		}
		return []wirePart{{Type: "text", Text: c}}
	case []wirePart:
		return c
	default:
		return nil
	}
}

func markBreakpoint(m *wireMessage) {
	parts := normalizeContent(m.Content)
	if len(parts) == 0 {
		return
	}
	last := parts[len(parts)-1]
	if last.Type != "text" {
		return
	}
	cc := wireCacheControl{Type: "ephemeral"}
	last.CacheControl = &cc
	parts[len(parts)-1] = last
	m.Content = parts
}

// applyCacheBreakpoints puts ephemeral cache_control markers on the stable
// prefix: static system instructions (not cwd/date, see llm.StablePrefix),
// the last tool schema, then the last content-bearing history message that
// is not the tail.
//
// Only the messages it marks are turned into content parts. Rewriting
// every message that way said nothing a server needed to hear — a plain
// string is the same content — and it made the whole history depend on
// whether the endpoint happened to be one of the two that honour
// breakpoints.
func applyCacheBreakpoints(w *wireRequest) {
	for i := range w.Messages {
		if w.Messages[i].Role == "system" {
			markStableSystem(&w.Messages[i])
			break
		}
	}
	if n := len(w.Tools); n > 0 {
		cc := wireCacheControl{Type: "ephemeral"}
		w.Tools[n-1].CacheControl = &cc
	}
	if len(w.Messages) < 2 {
		return
	}
	for i := len(w.Messages) - 2; i >= 0; i-- {
		if w.Messages[i].Role == "system" {
			continue
		}
		if len(normalizeContent(w.Messages[i].Content)) == 0 {
			continue
		}
		markBreakpoint(&w.Messages[i])
		break
	}
}

func markStableSystem(m *wireMessage) {
	parts := normalizeContent(m.Content)
	if len(parts) == 0 {
		return
	}
	if len(parts) == 1 && parts[0].Type == "text" {
		if stable, volatile := llm.StablePrefix(parts[0].Text); stable != "" {
			m.Content = []wirePart{
				{Type: "text", Text: stable, CacheControl: &wireCacheControl{Type: "ephemeral"}},
				{Type: "text", Text: volatile},
			}
			return
		}
	}
	markBreakpoint(m)
}

// stripCacheControl undoes applyCacheBreakpoints. A server rejects the
// marker either because it does not know the field or because it will
// not take array content at all, and the message cannot tell which, so
// this puts the request back exactly as it was before the breakpoints
// went on: the markers gone and every text-only message a plain string
// again, the split system prompt included. Leaving it as a one-element
// array — or as the two parts markStableSystem made of the system
// prompt — left a server of the second kind refusing every request for
// the rest of the session, having already spent its one retry.
func stripCacheControl(w *wireRequest) {
	for i := range w.Tools {
		w.Tools[i].CacheControl = nil
	}
	for i := range w.Messages {
		// A plain string never carried a marker, so only the messages a
		// breakpoint turned into parts need undoing.
		parts, ok := w.Messages[i].Content.([]wirePart)
		if !ok || len(parts) == 0 {
			continue
		}
		text, textOnly := strings.Builder{}, true
		for j := range parts {
			parts[j].CacheControl = nil
			if parts[j].Type != "text" || parts[j].ImageURL != nil {
				textOnly = false
				continue
			}
			text.WriteString(parts[j].Text)
		}
		if textOnly {
			w.Messages[i].Content = text.String()
			continue
		}
		w.Messages[i].Content = parts
	}
}

// reasoningCut marks reasoning that tailChars shortened.
const reasoningCut = "…"

// tailChars keeps the last max characters of s behind a marker; max <= 0
// keeps all of it. The result depends on s and max alone, so a message
// renders the same on every request and the prompt cache holds.
func tailChars(s string, max int) string {
	if max <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return reasoningCut + string(r[len(r)-max:])
}
