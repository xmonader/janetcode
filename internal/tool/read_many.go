package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/xmonader/janetcode/internal/textfmt"
)

// ReadManyFiles concatenates several text files into one result. It is
// deferred behind tool_search so its schema is not on every request.
type ReadManyFiles struct {
	Root  string
	WS    *Workspace
	Seen  *Seen
	Cache *IOCache
}

func (ReadManyFiles) Name() string { return "read_many_files" }
func (ReadManyFiles) Description() string {
	return "Read several text files in one call. Prefer glob/grep to find paths, then this. Max 20 files. Skips binaries."
}
func (ReadManyFiles) Mutating() bool { return false }
func (ReadManyFiles) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
"paths":{"type":"array","items":{"type":"string"},"description":"Files to read"},
"glob":{"type":"string","description":"Extra files by glob"}},
"additionalProperties":false}`)
}

const maxReadManyFiles = 20

func (t ReadManyFiles) Run(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Paths []string `json:"paths"`
		Glob  string   `json:"glob"`
	}
	if err := decode(input, &in); err != nil {
		return "", err
	}
	var files []string
	seen := map[string]bool{}
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		files = append(files, p)
	}
	for _, p := range in.Paths {
		add(p)
	}
	if g := strings.TrimSpace(in.Glob); g != "" {
		matched, err := t.globPaths(ctx, g)
		if err != nil {
			return "", err
		}
		for _, p := range matched {
			add(p)
		}
	}
	if len(files) == 0 {
		return "", fmt.Errorf("give paths or a glob")
	}
	sort.Strings(files)
	truncated := false
	if len(files) > maxReadManyFiles {
		files = files[:maxReadManyFiles]
		truncated = true
	}
	var sb strings.Builder
	for _, rel := range files {
		p, err := resolveOn(t.WS, t.Root, rel)
		if err != nil {
			fmt.Fprintf(&sb, "===== %s =====\n(error: %v)\n\n", rel, err)
			continue
		}
		if st, serr := statOf(ctx, t.WS, p); serr == nil && st.IsDir() {
			fmt.Fprintf(&sb, "===== %s =====\n(error: directory; list_dir or glob instead)\n\n", rel)
			continue
		}
		st, serr := statOf(ctx, t.WS, p)
		if serr == nil {
			if err := checkReadSize(rel, st.Size()); err != nil {
				fmt.Fprintf(&sb, "===== %s =====\n(error: %v)\n\n", rel, err)
				continue
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
			if err != nil {
				fmt.Fprintf(&sb, "===== %s =====\n(error: %v)\n\n", rel, err)
				continue
			}
			// The pre-read identity, as read_file does: see there.
			if serr == nil {
				t.Cache.Put(p, st.ModTime(), st.Size(), data)
			}
		}
		if isBinary(data) {
			fmt.Fprintf(&sb, "===== %s =====\n(error: binary file, %d bytes)\n\n", rel, len(data))
			continue
		}
		t.Seen.Record(p, data)
		text, shape := decodeText(data)
		fmt.Fprintf(&sb, "===== %s =====\n", rel)
		sb.WriteString(text)
		if !strings.HasSuffix(text, "\n") {
			sb.WriteByte('\n')
		}
		if note := shape.Note(); note != "" {
			fmt.Fprintf(&sb, "[%s]\n", note)
		}
		sb.WriteByte('\n')
	}
	if truncated {
		fmt.Fprintf(&sb, "(first %d files; narrow the glob)\n", maxReadManyFiles)
	}
	return textfmt.Truncate(sb.String()), nil
}

func (t ReadManyFiles) globPaths(ctx context.Context, pattern string) ([]string, error) {
	dir, err := resolveOn(t.WS, t.Root, ".")
	if err != nil {
		return nil, err
	}
	re, err := globToRegexp(pattern)
	if err != nil {
		return nil, err
	}
	var out []string
	err = walkOn(t.WS, ctx, dir, func(rel string, d fs.DirEntry) error {
		if d.IsDir() || !re.MatchString(rel) {
			return nil
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	return out, err
}
