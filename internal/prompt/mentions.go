package prompt

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// MaxMentionBytes caps how much of one @file is attached to a prompt.
const MaxMentionBytes = 100 << 10

// Attachment is one @path that was found and attached to a prompt.
type Attachment struct {
	Path string // as typed, relative to cwd
	Size int64
	Dir  bool
}

// ExpandMentions attaches the contents of every "@path" in text that names
// an existing file or directory under cwd. The user's text is left as typed;
// attachments follow it in <attached> blocks so the model sees both.
func ExpandMentions(cwd, text string) (string, []Attachment) {
	var (
		out  strings.Builder
		atts []Attachment
		seen = map[string]bool{}
	)
	for _, m := range mentionPaths(text) {
		if seen[m] {
			continue
		}
		full := m
		if !filepath.IsAbs(full) {
			full = filepath.Join(cwd, m)
		}
		fi, err := os.Stat(full)
		if err != nil {
			continue
		}
		seen[m] = true
		atts = append(atts, Attachment{Path: m, Size: fi.Size(), Dir: fi.IsDir()})
		fmt.Fprintf(&out, "\n\n<attached path=%q>\n%s\n</attached>", m, attachmentBody(full, fi))
	}
	if len(atts) == 0 {
		return text, nil
	}
	return text + out.String(), atts
}

func attachmentBody(full string, fi os.FileInfo) string {
	if fi.IsDir() {
		entries, err := os.ReadDir(full)
		if err != nil {
			return "(unreadable directory: " + err.Error() + ")"
		}
		var names []string
		for _, e := range entries {
			n := e.Name()
			if e.IsDir() {
				n += "/"
			}
			names = append(names, n)
		}
		if len(names) > 200 {
			names = append(names[:200], fmt.Sprintf("… %d more entries", len(names)-200))
		}
		return strings.Join(names, "\n")
	}
	f, err := os.Open(full)
	if err != nil {
		return "(unreadable: " + err.Error() + ")"
	}
	defer f.Close()
	buf := make([]byte, MaxMentionBytes+1)
	n, _ := f.Read(buf)
	buf = buf[:n]
	if bytes.IndexByte(buf, 0) >= 0 {
		return fmt.Sprintf("(binary file, %d bytes)", fi.Size())
	}
	if n > MaxMentionBytes {
		return string(buf[:MaxMentionBytes]) + fmt.Sprintf("\n… truncated, %d bytes total", fi.Size())
	}
	return string(buf)
}

// mentionPaths returns the paths named by @tokens in text, in order.
func mentionPaths(text string) []string {
	var paths []string
	for _, f := range strings.Fields(text) {
		if !strings.HasPrefix(f, "@") || len(f) < 2 {
			continue
		}
		p := strings.TrimRight(f[1:], ",.:;!?)]}'\"`")
		if p == "" {
			continue
		}
		paths = append(paths, p)
	}
	return paths
}

// CompletePath lists entries under cwd that could complete the partial path
// typed after an @. Directories end with "/". Hidden entries are offered only
// when the prefix asks for them.
func CompletePath(cwd, partial string) []string {
	dir, base := filepath.Split(partial)
	root := filepath.Join(cwd, dir)
	if filepath.IsAbs(partial) {
		root = dir
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, base) {
			continue
		}
		if strings.HasPrefix(name, ".") && !strings.HasPrefix(base, ".") {
			continue
		}
		if name == "node_modules" && base == "" {
			continue
		}
		if e.IsDir() {
			name += "/"
		}
		out = append(out, dir+name)
	}
	sort.Strings(out)
	return out
}
