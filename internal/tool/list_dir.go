package tool

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/xmonader/janetcode/internal/textfmt"
)

// ListDir lists a directory.
type ListDir struct {
	Root string
	WS   *Workspace
}

func (ListDir) Name() string { return "list_dir" }
func (ListDir) Description() string {
	return "List files and directories at a path (non-recursive). Directories end with /."
}
func (ListDir) Mutating() bool { return false }
func (ListDir) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
"path":{"type":"string","description":"Directory path (default: working directory)"}},
"additionalProperties":false}`)
}

func (t ListDir) Run(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Path string `json:"path"`
	}
	if len(input) > 0 {
		if err := decode(input, &in); err != nil {
			return "", err
		}
	}
	if in.Path == "" {
		in.Path = "."
	}
	p, err := resolveOn(t.WS, t.Root, in.Path)
	if err != nil {
		return "", err
	}
	entries, err := readDirOf(ctx, t.WS, p)
	if err != nil {
		return "", err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() {
			n += "/"
		}
		names = append(names, n)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return "(empty directory)", nil
	}
	return textfmt.Truncate(strings.Join(names, "\n")), nil
}
