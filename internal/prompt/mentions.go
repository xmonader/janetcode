package prompt

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// MaxMentionBytes caps how much of one @file is attached to a prompt.
const MaxMentionBytes = 100 << 10

// MaxImageBytes caps an attached image (providers reject larger ones).
const MaxImageBytes = 5 << 20

// Attachment is one @path that was found and attached to a prompt.
type Attachment struct {
	Path string // as typed, relative to cwd
	Size int64
	Dir  bool
	// Image attachments are not inlined in the text; the caller sends them
	// as image blocks. MediaType and Data (base64) are set for them.
	MediaType string
	Data      string
}

// Image reports whether the attachment is an image.
func (a Attachment) Image() bool { return a.MediaType != "" }

var imageTypes = map[string]string{".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif", ".webp": "image/webp"}

// ImageBlocks converts the image attachments to llm blocks.
func ImageBlocks(atts []Attachment) []llm.Block {
	var out []llm.Block
	for _, a := range atts {
		if a.Image() {
			out = append(out, llm.Block{Type: llm.BlockImage, MediaType: a.MediaType, Data: a.Data})
		}
	}
	return out
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
		if !fi.IsDir() && !fi.Mode().IsRegular() {
			// Opening a FIFO blocks until something writes to it, and a
			// device can stream forever: neither is read.
			fmt.Fprintf(&out, "\n\n<attached path=%q>\n(not a regular file: not read)\n</attached>", m)
			atts = append(atts, Attachment{Path: m})
			continue
		}
		if mt, ok := imageTypes[strings.ToLower(filepath.Ext(m))]; ok && !fi.IsDir() {
			data, err := os.ReadFile(full)
			if err != nil || len(data) > MaxImageBytes {
				fmt.Fprintf(&out, "\n\n<attached path=%q>\n(image not attached: %s)\n</attached>", m, imageProblem(err, len(data)))
				atts = append(atts, Attachment{Path: m, Size: fi.Size()})
				continue
			}
			atts = append(atts, Attachment{Path: m, Size: fi.Size(), MediaType: mt, Data: base64.StdEncoding.EncodeToString(data)})
			continue
		}
		atts = append(atts, Attachment{Path: m, Size: fi.Size(), Dir: fi.IsDir()})
		fmt.Fprintf(&out, "\n\n<attached path=%q>\n%s\n</attached>", m, attachmentBody(full, fi))
	}
	if len(atts) == 0 {
		return text, nil
	}
	return text + out.String(), atts
}

func imageProblem(err error, n int) string {
	if err != nil {
		return err.Error()
	}
	return fmt.Sprintf("%d bytes exceeds the %d MB limit", n, MaxImageBytes>>20)
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
	// One Read may return fewer bytes than are there (a network file
	// system, a file still being written), so read up to the cap in full.
	buf, err := io.ReadAll(io.LimitReader(f, MaxMentionBytes+1))
	if err != nil {
		return "(unreadable: " + err.Error() + ")"
	}
	n := len(buf)
	if bytes.IndexByte(buf, 0) >= 0 {
		return fmt.Sprintf("(binary file, %d bytes)", fi.Size())
	}
	if n > MaxMentionBytes {
		return textfmt.CutBytes(string(buf), MaxMentionBytes) + fmt.Sprintf("\n… truncated, %d bytes total", fi.Size())
	}
	return string(buf)
}

// mentionPaths returns the paths named by @tokens in text, in order. A
// mention starts at an "@" that opens a word and runs to the next space,
// less trailing punctuation; a path with a space in it is written quoted,
// @"my notes.md", which is how completion inserts one.
func mentionPaths(text string) []string {
	var paths []string
	for i := 0; i < len(text); i++ {
		if text[i] != '@' {
			continue
		}
		if r, _ := utf8.DecodeLastRuneInString(text[:i]); i > 0 && !unicode.IsSpace(r) {
			continue
		}
		rest := text[i+1:]
		if strings.HasPrefix(rest, `"`) {
			if j := strings.IndexByte(rest[1:], '"'); j > 0 && !strings.Contains(rest[1:1+j], "\n") {
				paths = append(paths, rest[1:1+j])
				i += 2 + j
				continue
			}
			// An unclosed quote: read the rest of the word bare.
			rest, i = rest[1:], i+1
		}
		end := strings.IndexFunc(rest, unicode.IsSpace)
		if end < 0 {
			end = len(rest)
		}
		if p := strings.TrimRight(rest[:end], ",.:;!?)]}'\"`"); p != "" {
			paths = append(paths, p)
		}
		i += end
	}
	return paths
}

// Mention is the @token that names path: bare when it can be, quoted
// when the path has a space in it. ok is false for a path no mention can
// name (one with a quote or a line break in it).
func Mention(path string) (token string, ok bool) {
	if path == "" || strings.ContainsAny(path, "\"\n\r") {
		return "", false
	}
	if strings.IndexFunc(path, unicode.IsSpace) >= 0 {
		return `@"` + path + `"`, true
	}
	return "@" + path, true
}

// MentionAtEnd finds an @mention being typed at the end of v: the partial
// path typed so far, the byte offset where the mention starts, and
// whether it was opened with a quote. A closed quoted mention is finished
// and does not count.
func MentionAtEnd(v string) (partial string, start int, quoted, ok bool) {
	if q := strings.LastIndex(v, `@"`); q >= 0 {
		r, _ := utf8.DecodeLastRuneInString(v[:q])
		tail := v[q+2:]
		if (q == 0 || unicode.IsSpace(r)) && !strings.ContainsAny(tail, "\"\n") {
			return tail, q, true, true
		}
	}
	i := strings.LastIndexFunc(v, unicode.IsSpace)
	word := v[i+1:]
	if i >= 0 {
		_, size := utf8.DecodeRuneInString(v[i:])
		word = v[i+size:]
	}
	if !strings.HasPrefix(word, "@") || strings.HasPrefix(word, `@"`) {
		return "", 0, false, false
	}
	return word[1:], len(v) - len(word), false, true
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
