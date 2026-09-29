package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/xmonader/janetcode/internal/textfmt"
)

// NotFound ends the error a read of a missing path returns. Looking for a
// file that is not there is an answer the model acts on, so front ends
// draw it as one rather than as a failed call (present.EndOf).
const NotFound = "does not exist"

// ReadFile reads a file with optional line window.
type ReadFile struct {
	Root string
	// WS, when set, is the shared workspace so /ssh retargets this tool
	// in place. Root is the fallback tests and worktrees still set.
	WS *Workspace
	// Seen, when set, records what was read for later conflict checks.
	Seen *Seen
	// Cache, when set, is shared with subagents so a second read of the
	// same unchanged file does not hit disk.
	Cache *IOCache
}

func (ReadFile) Name() string { return "read_file" }
func (ReadFile) Description() string {
	return "Read a text file. Returns numbered lines. Use offset/limit for large files (max 2000 lines per call). Set anchors to number them as 12:abc instead, where the letters mark the line's content: edit_file takes those in from_anchor/to_anchor and they keep pointing at the right line even after earlier edits move it."
}
func (ReadFile) Mutating() bool { return false }
func (ReadFile) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
"path":{"type":"string","description":"File path, relative to the working directory or absolute"},
"offset":{"type":"integer","description":"1-based line to start from (default 1)"},
"limit":{"type":"integer","description":"Max lines to return (default 2000)"},
"anchors":{"type":"boolean","description":"Number lines as 12:abc for edit_file from_anchor/to_anchor"}},
"required":["path"],"additionalProperties":false}`)
}

func (t ReadFile) Run(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Path    string `json:"path"`
		Offset  int    `json:"offset"`
		Limit   int    `json:"limit"`
		Anchors bool   `json:"anchors"`
	}
	if err := decode(input, &in); err != nil {
		return "", err
	}
	p, err := resolveOn(t.WS, t.Root, in.Path)
	if err != nil {
		return "", err
	}
	st, serr := statOf(ctx, t.WS, p)
	if serr == nil && st.IsDir() {
		return "", fmt.Errorf("%s is a directory; list_dir shows what is in it and glob finds files by pattern", in.Path)
	}
	if serr == nil {
		if err := checkReadSize(in.Path, st.Size()); err != nil {
			return "", err
		}
	}
	var data []byte
	if serr == nil {
		if cached, ok := t.Cache.Get(p, st.ModTime(), st.Size()); ok {
			data = cached
		}
	}
	if data == nil {
		var err error
		data, err = readFileOf(ctx, t.WS, p)
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("%s %s", in.Path, NotFound)
		}
		if err != nil {
			return "", err
		}
		// Filed under the identity stat'ed before the read, never after
		// it: a file rewritten while it was being read would otherwise
		// have its old bytes cached under its new mtime and size, and be
		// served stale until it next changed. Filed early, a change can
		// only make the entry miss.
		if serr == nil {
			t.Cache.Put(p, st.ModTime(), st.Size(), data)
		}
	}
	// A notebook is JSON, and read as JSON its code is a list of quoted
	// lines with the outputs — often base64 images — in between. Rendered
	// as cells it reads as the notebook it is. A file that does not parse
	// falls through and is shown as the text it is.
	if isNotebookPath(in.Path) {
		if nb, err := parseNotebook(data); err == nil {
			t.Seen.Record(p, data)
			return textfmt.Truncate(nb.render(in.Path)), nil
		}
	}
	if extracted, ok := extractReadable(ctx, in.Path, data); ok {
		t.Seen.Record(p, data)
		return extracted, nil
	}
	if isBinary(data) {
		return "", fmt.Errorf("%s is a binary file (%d bytes)", in.Path, len(data))
	}
	t.Seen.Record(p, data)
	text, shape := decodeText(data)
	lines := strings.Split(text, "\n")
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
		if in.Anchors {
			// The mark travels with the line, so an edit aimed at it still
			// lands after something above it moves.
			fmt.Fprintf(&sb, "%s\t%s\n", Anchor(i+1, lines[i]), lines[i])
		} else {
			fmt.Fprintf(&sb, "%d\t%s\n", i+1, lines[i])
		}
	}
	if end < len(lines) {
		fmt.Fprintf(&sb, "... (%d more lines)\n", len(lines)-end)
	}
	out := textfmt.Truncate(sb.String())
	if note := shape.Note(); note != "" {
		out += "[" + note + "]\n"
	}
	return out, nil
}

// isBinary reports whether the file cannot be handled as text: a NUL byte
// in the first 4 KB. UTF-16 is full of NUL bytes and is text, so a file
// that announces itself with a UTF-16 byte-order mark is exempt.
func isBinary(b []byte) bool {
	if bytes.HasPrefix(b, []byte(bomUTF16L)) || bytes.HasPrefix(b, []byte(bomUTF16B)) {
		return false
	}
	n := len(b)
	if n > binarySniffBytes {
		n = binarySniffBytes
	}
	return bytes.IndexByte(b[:n], 0) >= 0
}
