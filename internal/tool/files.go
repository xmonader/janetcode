package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ReadFile reads a file with optional line window.
type ReadFile struct{ Root string }

func (ReadFile) Name() string { return "read_file" }
func (ReadFile) Description() string {
	return "Read a text file. Returns numbered lines. Use offset/limit for large files (max 2000 lines per call)."
}
func (ReadFile) Mutating() bool { return false }
func (ReadFile) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
"path":{"type":"string","description":"File path, relative to the working directory or absolute"},
"offset":{"type":"integer","description":"1-based line to start from (default 1)"},
"limit":{"type":"integer","description":"Max lines to return (default 2000)"}},
"required":["path"],"additionalProperties":false}`)
}

func (t ReadFile) Run(_ context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if err := decode(input, &in); err != nil {
		return "", err
	}
	p, err := resolvePath(t.Root, in.Path)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	if isBinary(data) {
		return "", fmt.Errorf("%s is a binary file (%d bytes)", in.Path, len(data))
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if in.Offset < 1 {
		in.Offset = 1
	}
	if in.Limit < 1 || in.Limit > 2000 {
		in.Limit = 2000
	}
	if in.Offset > len(lines) {
		return fmt.Sprintf("(file has %d lines; offset %d is past the end)", len(lines), in.Offset), nil
	}
	end := in.Offset - 1 + in.Limit
	if end > len(lines) {
		end = len(lines)
	}
	var sb strings.Builder
	for i := in.Offset - 1; i < end; i++ {
		fmt.Fprintf(&sb, "%d\t%s\n", i+1, lines[i])
	}
	if end < len(lines) {
		fmt.Fprintf(&sb, "... (%d more lines)\n", len(lines)-end)
	}
	return Truncate(sb.String()), nil
}

func isBinary(b []byte) bool {
	n := len(b)
	if n > 8000 {
		n = 8000
	}
	for _, c := range b[:n] {
		if c == 0 {
			return true
		}
	}
	return false
}

// WriteFile creates or overwrites a file.
type WriteFile struct{ Root string }

func (WriteFile) Name() string { return "write_file" }
func (WriteFile) Description() string {
	return "Create or overwrite a file with the given content. Parent directories are created. Prefer edit_file for changes to existing files."
}
func (WriteFile) Mutating() bool { return true }
func (WriteFile) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
"path":{"type":"string","description":"File path"},
"content":{"type":"string","description":"Full file content"}},
"required":["path","content"],"additionalProperties":false}`)
}

func (t WriteFile) Run(_ context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := decode(input, &in); err != nil {
		return "", err
	}
	p, err := resolvePath(t.Root, in.Path)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	existed := fileExists(p)
	if err := atomicWrite(p, []byte(in.Content)); err != nil {
		return "", err
	}
	verb := "Created"
	if existed {
		verb = "Overwrote"
	}
	return fmt.Sprintf("%s %s (%d bytes)", verb, in.Path, len(in.Content)), nil
}

// atomicWrite writes via a temp file + rename so a crash mid-write never
// leaves a truncated file. Preserves the mode of an existing file.
func atomicWrite(p string, data []byte) error {
	mode := os.FileMode(0o644)
	if st, err := os.Stat(p); err == nil {
		mode = st.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), "."+filepath.Base(p)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, p); err != nil {
		cleanup()
		return err
	}
	return nil
}

// EditFile replaces an exact substring in a file.
type EditFile struct{ Root string }

func (EditFile) Name() string { return "edit_file" }
func (EditFile) Description() string {
	return "Replace an exact string in a file. old_string must match exactly once (including whitespace) unless replace_all is true. Fails if no match or ambiguous."
}
func (EditFile) Mutating() bool { return true }
func (EditFile) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
"path":{"type":"string","description":"File path"},
"old_string":{"type":"string","description":"Exact text to find"},
"new_string":{"type":"string","description":"Replacement text"},
"replace_all":{"type":"boolean","description":"Replace every occurrence (default false)"}},
"required":["path","old_string","new_string"],"additionalProperties":false}`)
}

func (t EditFile) Run(_ context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Path       string `json:"path"`
		Old        string `json:"old_string"`
		New        string `json:"new_string"`
		ReplaceAll bool   `json:"replace_all"`
	}
	if err := decode(input, &in); err != nil {
		return "", err
	}
	if in.Old == "" {
		return "", fmt.Errorf("old_string must not be empty")
	}
	if in.Old == in.New {
		return "", fmt.Errorf("old_string and new_string are identical")
	}
	p, err := resolvePath(t.Root, in.Path)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	content := string(data)
	n := strings.Count(content, in.Old)
	switch {
	case n == 0:
		return "", fmt.Errorf("old_string not found in %s", in.Path)
	case n > 1 && !in.ReplaceAll:
		return "", fmt.Errorf("old_string matches %d times in %s; make it unique or set replace_all", n, in.Path)
	}
	var updated string
	if in.ReplaceAll {
		updated = strings.ReplaceAll(content, in.Old, in.New)
	} else {
		updated = strings.Replace(content, in.Old, in.New, 1)
	}
	if err := atomicWrite(p, []byte(updated)); err != nil {
		return "", err
	}
	return fmt.Sprintf("Edited %s (%d replacement(s))", in.Path, n), nil
}

// ListDir lists a directory.
type ListDir struct{ Root string }

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

func (t ListDir) Run(_ context.Context, input json.RawMessage) (string, error) {
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
	p, err := resolvePath(t.Root, in.Path)
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(p)
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
	return Truncate(strings.Join(names, "\n")), nil
}
