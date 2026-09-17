package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
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

// maxDirEntries bounds one listing. A directory with hundreds of
// thousands of entries would otherwise be read, sorted and joined in
// full for output that textfmt.Truncate clips anyway; past the cap the
// model is told the listing is partial and glob can take over.
const maxDirEntries = 2000

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
	entries, over, err := readDirCapped(ctx, t.WS, p, maxDirEntries)
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
	if over {
		names = append(names, fmt.Sprintf("(listing capped at %d entries — narrow the path or use glob)", maxDirEntries))
	}
	if len(names) == 0 {
		return "(empty directory)", nil
	}
	return strings.Join(names, "\n"), nil
}

// readDirCapped returns at most max entries of a directory, plus whether
// more exist. On the local host it stops reading the directory stream at
// max+1, so a huge directory costs a bounded read instead of a full
// materialize-sort-clip; a remote host has already paid for the full
// listing by the time Host.ReadDir returns, so its result is capped in
// memory. The retained entries keep directory order; the caller sorts.
func readDirCapped(ctx context.Context, ws *Workspace, path string, max int) (entries []os.DirEntry, over bool, err error) {
	if ws == nil {
		f, err := os.Open(path)
		if err != nil {
			return nil, false, err
		}
		defer f.Close()
		entries, err = f.ReadDir(max + 1)
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, false, err
		}
		if len(entries) > max {
			return entries[:max], true, nil
		}
		return entries, false, nil
	}
	entries, err = ws.Host().ReadDir(ctx, path)
	if err != nil || len(entries) <= max {
		return entries, false, err
	}
	return entries[:max], true, nil
}
