package gemini

import (
	"encoding/json"
	"strings"

	"github.com/xmonader/janetcode/internal/llm"
)

type part struct {
	Text             string            `json:"text,omitempty"`
	Thought          bool              `json:"thought,omitempty"`
	ThoughtSignature string            `json:"thoughtSignature,omitempty"`
	InlineData       *blob             `json:"inlineData,omitempty"`
	FunctionCall     *functionCall     `json:"functionCall,omitempty"`
	FunctionResponse *functionResponse `json:"functionResponse,omitempty"`
}

type blob struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

type functionCall struct {
	ID   string          `json:"id,omitempty"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"`
}

type functionResponse struct {
	ID       string         `json:"id,omitempty"`
	Name     string         `json:"name"`
	Response map[string]any `json:"response"`
}

type content struct {
	Role  string `json:"role,omitempty"`
	Parts []part `json:"parts"`
}

type functionDecl struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parametersJsonSchema,omitempty"`
}

type toolDecls struct {
	FunctionDeclarations []functionDecl `json:"functionDeclarations"`
}

type thinkingConfig struct {
	IncludeThoughts bool   `json:"includeThoughts,omitempty"`
	ThinkingLevel   string `json:"thinkingLevel,omitempty"`
}

type generationConfig struct {
	MaxOutputTokens    int             `json:"maxOutputTokens,omitempty"`
	ThinkingConfig     *thinkingConfig `json:"thinkingConfig,omitempty"`
	ResponseMimeType   string          `json:"responseMimeType,omitempty"`
	ResponseJSONSchema json.RawMessage `json:"responseJsonSchema,omitempty"`
}

type request struct {
	Contents          []content         `json:"contents"`
	SystemInstruction *content          `json:"systemInstruction,omitempty"`
	Tools             []toolDecls       `json:"tools,omitempty"`
	GenerationConfig  *generationConfig `json:"generationConfig,omitempty"`
}

type buildOpts struct {
	thoughts, level, thinking, schema, sentinel bool
}

// levels maps janetcode's effort tiers onto Gemini's thinking levels.
// The two tiers above high have no Gemini equivalent and ask for its most.
var levels = map[string]string{"low": "LOW", "medium": "MEDIUM", "high": "HIGH", "xhigh": "HIGH", "max": "HIGH"}

func buildRequest(req llm.Request, o buildOpts) request {
	r := request{Contents: contents(req.Messages, o.sentinel)}
	if strings.TrimSpace(req.System) != "" {
		r.SystemInstruction = &content{Parts: []part{{Text: req.System}}}
	}
	if len(req.Tools) > 0 {
		decls := make([]functionDecl, 0, len(req.Tools))
		for _, t := range req.Tools {
			decls = append(decls, functionDecl{Name: t.Name, Description: t.Description, Parameters: t.InputSchema})
		}
		r.Tools = []toolDecls{{FunctionDeclarations: decls}}
	}
	gc := &generationConfig{MaxOutputTokens: req.MaxTokens}
	if o.thinking {
		tc := &thinkingConfig{IncludeThoughts: o.thoughts}
		if o.level {
			tc.ThinkingLevel = levels[req.Effort]
		}
		if tc.IncludeThoughts || tc.ThinkingLevel != "" {
			gc.ThinkingConfig = tc
		}
	}
	if req.Schema != nil && o.schema {
		gc.ResponseMimeType = "application/json"
		gc.ResponseJSONSchema = req.Schema.JSON
	}
	if gc.MaxOutputTokens > 0 || gc.ThinkingConfig != nil || gc.ResponseMimeType != "" {
		r.GenerationConfig = gc
	}
	return r
}

// contents converts the history. Tool results need the name of the call
// they answer, which only the assistant message that made it knows, so
// the calls are indexed first.
func contents(msgs []llm.Message, sentinel bool) []content {
	calls := map[string]llm.Block{}
	for _, m := range msgs {
		for _, b := range m.ToolUses() {
			calls[b.ID] = b
		}
	}
	out := make([]content, 0, len(msgs))
	for _, m := range msgs {
		var c content
		if m.Role == llm.RoleAssistant {
			c = modelContent(m, sentinel)
		} else {
			c = userContent(m, calls)
		}
		if len(c.Parts) > 0 {
			out = append(out, c)
		}
	}
	return out
}

func modelContent(m llm.Message, sentinel bool) content {
	c := content{Role: "model"}
	signed := false
	firstCall := -1
	for _, b := range m.Blocks {
		switch b.Type {
		case llm.BlockText:
			if b.Text == "" && b.Signature == "" {
				continue
			}
			c.Parts = append(c.Parts, part{Text: b.Text, ThoughtSignature: b.Signature})
		case llm.BlockToolUse:
			args := b.Input
			if len(strings.TrimSpace(string(args))) == 0 {
				args = json.RawMessage(`{}`)
			}
			id := b.WireID()
			if strings.HasPrefix(id, callPrefix) {
				id = ""
			}
			if firstCall < 0 {
				firstCall = len(c.Parts)
			}
			c.Parts = append(c.Parts, part{FunctionCall: &functionCall{ID: id, Name: b.Name, Args: args}, ThoughtSignature: b.Signature})
		}
		// Thinking blocks are not sent back: Gemini's thought summaries
		// are for reading, and what the model needs of its reasoning
		// rides the signatures on the parts above.
		if b.Signature != "" && b.Type != llm.BlockThinking {
			signed = true
		}
	}
	if sentinel && !signed && firstCall >= 0 {
		c.Parts[firstCall].ThoughtSignature = skipSignature
	}
	return c
}

func userContent(m llm.Message, calls map[string]llm.Block) content {
	c := content{Role: "user"}
	for _, b := range m.Blocks {
		switch b.Type {
		case llm.BlockText:
			if b.Text != "" {
				c.Parts = append(c.Parts, part{Text: b.Text})
			}
		case llm.BlockImage:
			c.Parts = append(c.Parts, part{InlineData: &blob{MimeType: b.MediaType, Data: b.Data}})
		case llm.BlockToolResult:
			call, ok := calls[b.ToolUseID]
			name, id := call.Name, call.WireID()
			if !ok {
				// A result whose call was compacted away; the API still
				// wants a name, and an id it never issued would be wrong.
				name, id = "tool", ""
			}
			if strings.HasPrefix(id, callPrefix) {
				id = ""
			}
			key := "result"
			if b.IsError {
				key = "error"
			}
			c.Parts = append(c.Parts, part{FunctionResponse: &functionResponse{ID: id, Name: name, Response: map[string]any{key: b.Text}}})
		}
	}
	return c
}
