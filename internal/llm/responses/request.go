package responses

import (
	"encoding/json"
	"strings"

	"github.com/xmonader/janetcode/internal/llm"
)

type request struct {
	Model           string     `json:"model"`
	Instructions    string     `json:"instructions,omitempty"`
	Input           []any      `json:"input"`
	Stream          bool       `json:"stream"`
	Store           *bool      `json:"store,omitempty"`
	Include         []string   `json:"include,omitempty"`
	MaxOutputTokens int        `json:"max_output_tokens,omitempty"`
	Tools           []tool     `json:"tools,omitempty"`
	Reasoning       *reasoning `json:"reasoning,omitempty"`
	Text            *textOpts  `json:"text,omitempty"`
}

type tool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
	Strict      bool            `json:"strict"`
}

type reasoning struct {
	Effort  string `json:"effort,omitempty"`
	Summary string `json:"summary,omitempty"`
}

type textOpts struct {
	Format *format `json:"format,omitempty"`
}

type format struct {
	Type   string          `json:"type"`
	Name   string          `json:"name"`
	Schema json.RawMessage `json:"schema"`
	Strict bool            `json:"strict"`
}

type message struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type contentPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
}

type reasoningItem struct {
	Type             string        `json:"type"`
	ID               string        `json:"id"`
	Summary          []summaryPart `json:"summary"`
	EncryptedContent string        `json:"encrypted_content"`
}

type summaryPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type functionCall struct {
	Type      string `json:"type"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type functionOutput struct {
	Type   string `json:"type"`
	CallID string `json:"call_id"`
	Output string `json:"output"`
}

type buildOpts struct {
	thoughts, reasoning, xhigh, include, store, schema bool
}

// efforts maps janetcode's tiers onto the API's. "max" has no equivalent
// and asks for the most there is.
var efforts = map[string]string{"low": "low", "medium": "medium", "high": "high", "xhigh": "xhigh", "max": "xhigh"}

func buildRequest(req llm.Request, o buildOpts) request {
	f := false
	r := request{Model: req.Model, Instructions: req.System, Input: input(req.Messages), Stream: true, MaxOutputTokens: req.MaxTokens}
	if o.store {
		// Nothing kept server-side: the history is ours, and with store
		// on the encrypted reasoning is not returned at all.
		r.Store = &f
	}
	if o.reasoning {
		e := efforts[req.Effort]
		if e == "xhigh" && !o.xhigh {
			e = "high"
		}
		rs := &reasoning{Effort: e}
		if o.thoughts {
			rs.Summary = "auto"
		}
		if rs.Effort != "" || rs.Summary != "" {
			r.Reasoning = rs
		}
		if o.include {
			r.Include = []string{"reasoning.encrypted_content"}
		}
	}
	for _, t := range req.Tools {
		params := t.InputSchema
		if len(strings.TrimSpace(string(params))) == 0 {
			params = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		r.Tools = append(r.Tools, tool{Type: "function", Name: t.Name, Description: t.Description, Parameters: params})
	}
	if req.Schema != nil && o.schema {
		name := req.Schema.Name
		if name == "" {
			name = "answer"
		}
		r.Text = &textOpts{Format: &format{Type: "json_schema", Name: name, Schema: req.Schema.JSON, Strict: req.Schema.Strict}}
	}
	return r
}

// input converts the history into Responses input items, keeping each
// assistant message's blocks in the order the model produced them: a
// reasoning item has to precede the call it led to.
func input(msgs []llm.Message) []any {
	calls := map[string]string{}
	for _, m := range msgs {
		for _, b := range m.ToolUses() {
			calls[b.ID] = b.WireID()
		}
	}
	var out []any
	for _, m := range msgs {
		if m.Role == llm.RoleAssistant {
			out = append(out, assistantItems(m)...)
			continue
		}
		var parts []contentPart
		flush := func() {
			if len(parts) > 0 {
				out = append(out, message{Role: "user", Content: parts})
				parts = nil
			}
		}
		for _, b := range m.Blocks {
			switch b.Type {
			case llm.BlockText:
				if b.Text != "" {
					parts = append(parts, contentPart{Type: "input_text", Text: b.Text})
				}
			case llm.BlockImage:
				parts = append(parts, contentPart{Type: "input_image", ImageURL: "data:" + b.MediaType + ";base64," + b.Data})
			case llm.BlockToolResult:
				// Results answer calls, and a call's output must follow it
				// as its own item, not inside a user message.
				flush()
				id := b.ToolUseID
				if w, ok := calls[id]; ok {
					id = w
				}
				text := b.Text
				if b.IsError && !strings.HasPrefix(text, "Error") {
					text = "Error: " + text
				}
				out = append(out, functionOutput{Type: "function_call_output", CallID: id, Output: text})
			}
		}
		flush()
	}
	return out
}

func assistantItems(m llm.Message) []any {
	var out []any
	var text strings.Builder
	flush := func() {
		if text.Len() > 0 {
			out = append(out, message{Role: "assistant", Content: text.String()})
			text.Reset()
		}
	}
	for _, b := range m.Blocks {
		switch b.Type {
		case llm.BlockText:
			text.WriteString(b.Text)
		case llm.BlockThinking:
			if !strings.HasPrefix(b.ID, reasoningPrefix) || b.Signature == "" {
				continue // another provider's thinking, or none to replay
			}
			flush()
			sum := []summaryPart{}
			if b.Text != "" {
				sum = append(sum, summaryPart{Type: "summary_text", Text: b.Text})
			}
			out = append(out, reasoningItem{Type: "reasoning", ID: b.ID, Summary: sum, EncryptedContent: b.Signature})
		case llm.BlockToolUse:
			flush()
			args := string(b.Input)
			if strings.TrimSpace(args) == "" {
				args = "{}"
			}
			out = append(out, functionCall{Type: "function_call", CallID: b.WireID(), Name: b.Name, Arguments: args})
		}
	}
	flush()
	return out
}
