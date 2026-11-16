package agent

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/xmonader/janetcode/internal/tool"
)

// Discovery does not activate tools in the model's schema array. Scripts can
// inspect and call deferred tools without changing the cached request prefix.
type scriptToolInfo struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

type scriptReply struct {
	Output string          `json:"output,omitempty"`
	Data   json.RawMessage `json:"data,omitempty"`
	Error  string          `json:"error,omitempty"`
	Kind   string          `json:"kind,omitempty"`
}

func (r *rpc) discover(operation, query string) scriptReply {
	if r.parent == nil || r.parent.Tools == nil {
		return scriptReply{Error: "no tool registry", Kind: "unavailable"}
	}
	reg := r.parent.Tools
	if operation == "describe" {
		t, ok := reg.Get(query)
		if !ok || scriptSkip[query] {
			return scriptReply{Error: fmt.Sprintf("tool %q is unavailable to scripts", query), Kind: "unknown"}
		}
		data, err := json.Marshal(scriptToolInfo{Name: query, Description: t.Description(), InputSchema: t.Schema()})
		if err != nil {
			return scriptReply{Error: err.Error(), Kind: "tool"}
		}
		return scriptReply{Data: data}
	}
	var found []scriptToolInfo
	words := strings.Fields(strings.ToLower(query))
	names := append(reg.Names(), reg.Loadable()...)
	slices.Sort(names)
	names = slices.Compact(names)
	for _, name := range names {
		if scriptSkip[name] {
			continue
		}
		t, ok := reg.Get(name)
		if !ok {
			continue
		}
		description := t.Description()
		hay := strings.ToLower(name + " " + description)
		matches := true
		for _, word := range words {
			if !strings.Contains(hay, word) {
				matches = false
				break
			}
		}
		if !matches {
			continue
		}
		if i := strings.IndexByte(description, '\n'); i >= 0 {
			description = description[:i]
		}
		if len(description) > 240 {
			description = description[:runeStartAtOrBefore(description, 240)] + "…"
		}
		found = append(found, scriptToolInfo{Name: name, Description: description})
	}
	// Keep large MCP catalogs outside context unless the script selects them.
	total := len(found)
	if len(found) > 20 {
		found = found[:20]
	}
	if found == nil {
		found = []scriptToolInfo{}
	}
	data, err := json.Marshal(struct {
		Tools     []scriptToolInfo `json:"tools"`
		Total     int              `json:"total"`
		Truncated bool             `json:"truncated"`
	}{found, total, total > len(found)})
	if err != nil {
		return scriptReply{Error: err.Error(), Kind: "tool"}
	}
	return scriptReply{Data: data}
}

// scriptResult keeps presentation and structured output separate.
func scriptResult(res tool.Result) scriptReply { return scriptReply{Output: res.Text, Data: res.Data} }
