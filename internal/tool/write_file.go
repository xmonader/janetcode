package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/xmonader/janetcode/internal/diff"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// WriteFile creates or overwrites a file.
type WriteFile struct {
	Root string
	WS   *Workspace
	// Seen, when set, refuses to overwrite a file that changed since the
	// agent last read or wrote it.
	Seen  *Seen
	Cache *IOCache
	// ThenRun advertises then_run (see ThenRunKey).
	ThenRun bool
}

func (WriteFile) Name() string { return "write_file" }
func (WriteFile) Description() string {
	return "Create or overwrite a file with the given content. Parent directories are created. Prefer edit_file for changes to existing files."
}
func (WriteFile) Mutating() bool { return true }
func (t WriteFile) Schema() json.RawMessage {
	thenRun := ""
	if t.ThenRun {
		thenRun = thenRunProp
	}
	return json.RawMessage(`{"type":"object","properties":{
"path":{"type":"string","description":"File path, relative to the working directory or absolute"},
` + thenRun + `"content":{"type":"string","description":"Full file content"}},
"required":["path","content"],"additionalProperties":false}`)
}

func (t WriteFile) Run(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := decode(input, &in); err != nil {
		return "", err
	}
	if err := checkWriteSize("content", len(in.Content), "write the head with write_file and append the rest with edit_file"); err != nil {
		return "", err
	}
	p, err := resolveOn(t.WS, t.Root, in.Path)
	if err != nil {
		return "", err
	}
	// The conflict check comes first: a refused write must leave the tree
	// exactly as it found it, not with the directories it would have
	// needed already made.
	if err := t.Seen.CheckAt(ctx, t.WS, p, in.Path, "write"); err != nil {
		return "", err
	}
	if err := mkdirAllOf(ctx, t.WS, filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	old, readErr := readFileOf(ctx, t.WS, p)
	existed := readErr == nil
	// A file keeps the shape it had: its byte-order mark, its line endings
	// and its charset. Otherwise the first write to a Windows or Latin-1
	// repository rewrites every line of the file it touched.
	oldText, shape := "", form{Charset: "utf-8"}
	if existed {
		oldText, shape = decodeText(old)
	}
	data, err := encodeChecked(in.Content, shape, "content", "write the head with write_file and append the rest with edit_file")
	if err != nil {
		return "", err
	}
	if err := writeFileOf(ctx, t.WS, p, data); err != nil {
		return "", err
	}
	t.Cache.Drop(p)
	t.Seen.Record(p, data)
	root := rootOf(t.Root, t.WS)
	if !existed {
		return fmt.Sprintf("Created %s (%s)", displayPath(root, in.Path), textfmt.Plural(countLines(in.Content), "line")), nil
	}
	added, removed := diff.Stats(oldText, in.Content)
	out := fmt.Sprintf("Overwrote %s (+%d -%d lines)", displayPath(root, in.Path), added, removed)
	if note := shape.Note(); note != "" {
		out += "\n[" + note + "]"
	}
	return out, nil
}

// Guard implements Guard: a write to one of janetcode's own control
// surfaces asks, whatever the permission mode.
func (t WriteFile) Guard(input json.RawMessage) (bool, string) {
	return guardSelfModify(rootOf(t.Root, t.WS), input)
}

// Touches reports the file the write will replace or create.
func (t WriteFile) Touches(input json.RawMessage) ([]string, error) {
	return touchedPathOn(t.WS, t.Root, input)
}

// Result computes what this write would produce, without writing: the
// resolved path, and the file's content before (empty for a file that
// does not exist yet) and after. internal/impact uses it to analyze the
// reach of a write before it is approved. The read it does to work out
// "before" can cross a network (a remote workspace), so it takes the
// caller's context rather than inventing one.
func (t WriteFile) Result(ctx context.Context, input json.RawMessage) (path, before, after string, err error) {
	var in struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := decode(input, &in); err != nil {
		return "", "", "", err
	}
	p, err := resolveOn(t.WS, t.Root, in.Path)
	if err != nil {
		return "", "", "", err
	}
	if old, rerr := readFileOf(ctx, t.WS, p); rerr == nil && !isBinary(old) {
		before, _ = decodeText(old)
	}
	return p, before, in.Content, nil
}

// Preview implements Previewer: a diff against the existing file, or the
// head of a new file.
func (t WriteFile) Preview(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := decode(input, &in); err != nil {
		return "", err
	}
	p, err := resolveOn(t.WS, t.Root, in.Path)
	if err != nil {
		return "", err
	}
	old, err := readFileOf(ctx, t.WS, p)
	if err != nil {
		return diff.Unified(in.Path, "", in.Content, 0), nil
	}
	if isBinary(old) {
		return fmt.Sprintf("(replacing binary file %s, %d bytes)", in.Path, len(old)), nil
	}
	oldText, _ := decodeText(old)
	d := diff.Unified(in.Path, oldText, in.Content, 3)
	if d == "" {
		// The line diff cannot see a final newline added or dropped, and
		// calling that "identical" in a permission prompt is false.
		if oldText != in.Content {
			return "(only the newline at the end of the file changes)", nil
		}
		return "(no change: content is identical)", nil
	}
	return d, nil
}

func countLines(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}

// touchedPathOn resolves the "path" field of a file tool's input.
func touchedPathOn(ws *Workspace, root string, input json.RawMessage) ([]string, error) {
	var in struct {
		Path string `json:"path"`
	}
	// Plain unmarshal: only the path matters here, the other fields are the
	// tool's business.
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, err
	}
	p, err := resolveOn(ws, root, in.Path)
	if err != nil {
		return nil, err
	}
	return []string{p}, nil
}
