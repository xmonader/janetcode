package tool

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/xmonader/janetcode/internal/textfmt"
)

// skipDirs are never descended into by glob/grep.
var skipDirs = map[string]bool{
	".git": true, "node_modules": true, ".venv": true, "venv": true,
	"__pycache__": true, "target": true, "dist": true, ".idea": true, ".cache": true,
}

// Glob finds files matching a glob pattern.
type Glob struct {
	Root string
	WS   *Workspace
}

func (Glob) Name() string { return "glob" }
func (Glob) Description() string {
	return "Find files by glob pattern, e.g. \"**/*.go\" or \"src/**/test_*.py\". Supports **, *, ?, and {a,b}. Skips .git, node_modules and build dirs. Returns paths relative to the search directory, newest first."
}
func (Glob) Mutating() bool { return false }
func (Glob) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
"pattern":{"type":"string","description":"Glob pattern"},
"path":{"type":"string","description":"Directory to search (default: working directory)"}},
"required":["pattern"],"additionalProperties":false}`)
}

const maxGlobResults = 1000

func (t Glob) Run(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
	}
	if err := decode(input, &in); err != nil {
		return "", err
	}
	if in.Path == "" {
		in.Path = "."
	}
	dir, err := resolveOn(t.WS, t.Root, in.Path)
	if err != nil {
		return "", err
	}
	re, err := globToRegexp(in.Pattern)
	if err != nil {
		return "", err
	}
	type hit struct {
		path string
		mod  int64
	}
	var hits []hit
	err = walkOn(t.WS, ctx, dir, func(rel string, d fs.DirEntry) error {
		if d.IsDir() || !re.MatchString(rel) {
			return nil
		}
		info, ierr := d.Info()
		var mod int64
		if ierr == nil {
			mod = info.ModTime().UnixNano()
		}
		hits = append(hits, hit{rel, mod})
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].mod > hits[j].mod })
	if len(hits) == 0 {
		return "No files matched.", nil
	}
	truncated := false
	if len(hits) > maxGlobResults {
		hits = hits[:maxGlobResults]
		truncated = true
	}
	var sb strings.Builder
	for _, h := range hits {
		sb.WriteString(h.path)
		sb.WriteByte('\n')
	}
	if truncated {
		fmt.Fprintf(&sb, "(showing first %d matches; narrow the pattern)\n", maxGlobResults)
	}
	return textfmt.Truncate(sb.String()), nil
}

// walkOn visits every entry under dir, skipping skipDirs, calling fn with
// the slash-separated path relative to dir.
func walkOn(ws *Workspace, ctx context.Context, dir string, fn func(rel string, d fs.DirEntry) error) error {
	return walkDirOf(ctx, ws, dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if p == dir {
			return nil
		}
		if d.IsDir() && skipDirs[d.Name()] {
			return fs.SkipDir
		}
		rel, rerr := filepath.Rel(dir, p)
		if rerr != nil {
			return nil
		}
		return fn(filepath.ToSlash(rel), d)
	})
}

// globToRegexp converts a glob with **, *, ?, {a,b} into an anchored regexp.
func globToRegexp(pattern string) (*regexp.Regexp, error) {
	if strings.TrimSpace(pattern) == "" {
		return nil, fmt.Errorf("pattern is empty")
	}
	pattern = strings.TrimPrefix(pattern, "./")
	var sb strings.Builder
	sb.WriteString("^")
	inBrace := 0
	for i := 0; i < len(pattern); i++ {
		c := pattern[i]
		switch c {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				i++
				// "**/" matches zero or more directories; trailing "**" matches anything.
				if i+1 < len(pattern) && pattern[i+1] == '/' {
					i++
					sb.WriteString("(?:.*/)?")
				} else {
					sb.WriteString(".*")
				}
			} else {
				sb.WriteString("[^/]*")
			}
		case '?':
			sb.WriteString("[^/]")
		case '{':
			inBrace++
			sb.WriteString("(?:")
		case '}':
			if inBrace == 0 {
				return nil, fmt.Errorf("unbalanced } in pattern")
			}
			inBrace--
			sb.WriteString(")")
		case ',':
			if inBrace > 0 {
				sb.WriteString("|")
			} else {
				sb.WriteString(",")
			}
		default:
			sb.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	if inBrace != 0 {
		return nil, fmt.Errorf("unbalanced { in pattern")
	}
	sb.WriteString("$")
	return regexp.Compile(sb.String())
}

// Grep searches file contents with a regular expression.
type Grep struct {
	Root string
	WS   *Workspace
}

func (Grep) Name() string { return "grep" }
func (Grep) Description() string {
	return "Search file contents with a Go/RE2 regular expression. Returns path:line:text. Optionally restrict to files matching a glob. Skips binary files and .git/node_modules."
}
func (Grep) Mutating() bool { return false }
func (Grep) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
"pattern":{"type":"string","description":"Regular expression"},
"path":{"type":"string","description":"Directory or file to search (default: working directory)"},
"glob":{"type":"string","description":"Only search files matching this glob, e.g. \"*.go\""},
"case_insensitive":{"type":"boolean","description":"Ignore case (default false)"}},
"required":["pattern"],"additionalProperties":false}`)
}

const (
	maxGrepResults = 500
	maxGrepLine    = 500
	// maxScanLine is how much of one line is searched; maxLongNotes how
	// many lines past it the result names one by one.
	maxScanLine  = 1 << 20
	maxLongNotes = 5
)

func (t Grep) Run(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Pattern         string `json:"pattern"`
		Path            string `json:"path"`
		Glob            string `json:"glob"`
		CaseInsensitive bool   `json:"case_insensitive"`
	}
	if err := decode(input, &in); err != nil {
		return "", err
	}
	if in.Pattern == "" {
		return "", fmt.Errorf("pattern is empty")
	}
	expr := in.Pattern
	if in.CaseInsensitive {
		expr = "(?i)" + expr
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return "", fmt.Errorf("invalid regex: %w", err)
	}
	var fileRe *regexp.Regexp
	if in.Glob != "" {
		// A bare "*.go" should match at any depth.
		g := in.Glob
		if !strings.Contains(g, "/") {
			g = "**/" + g
		}
		if fileRe, err = globToRegexp(g); err != nil {
			return "", err
		}
	}
	if in.Path == "" {
		in.Path = "."
	}
	target, err := resolveOn(t.WS, t.Root, in.Path)
	if err != nil {
		return "", err
	}
	st, err := statOf(ctx, t.WS, target)
	if err != nil {
		return "", err
	}
	var sb, long strings.Builder
	count, longLines := 0, 0
	head := make([]byte, 8000)
	br := bufio.NewReaderSize(nil, 64<<10)
	var lineBuf []byte
	grepFile := func(display, full string) error {
		f, err := openOf(ctx, t.WS, full)
		if err != nil {
			return nil
		}
		defer f.Close()
		// head, br and lineBuf are shared by every file of the search:
		// grepFile runs one file at a time.
		// ReadFull, not Read: one Read may come back with a handful of
		// bytes from a pipe or a slow filesystem, and a binary file whose
		// first NUL sits past them would then be scanned as text.
		n, err := io.ReadFull(f, head)
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return nil
		}
		if isBinary(head[:n]) {
			return nil
		}
		// Not a bufio.Scanner: on a line longer than its buffer (a
		// minified bundle, a data blob) it stops, and every match after
		// that line went unreported. A long line is searched up to
		// maxScanLine and the rest of it skipped, which the result says.
		br.Reset(io.MultiReader(bytes.NewReader(head[:n]), f))
		ln := 0
		for {
			raw, clipped, rerr := readLineCapped(br, maxScanLine, lineBuf)
			lineBuf = raw
			if rerr != nil && len(raw) == 0 && !clipped {
				return nil // end of file, or a read error: stop this file
			}
			ln++
			if clipped {
				if longLines < maxLongNotes {
					fmt.Fprintf(&long, "(%s:%d is longer than %d MiB; only its first %d MiB was searched)\n", display, ln, maxScanLine>>20, maxScanLine>>20)
				}
				longLines++
			}
			// Matched on the bytes: a string per line was most of what a
			// search of a large tree allocated (1 GB for one identifier
			// across kubernetes), and only a matching line needs one.
			if re.Match(raw) {
				fmt.Fprintf(&sb, "%s:%d:%s\n", display, ln, textfmt.ClipBytes(string(raw), maxGrepLine))
				count++
				if count >= maxGrepResults {
					return errLimit
				}
			}
			if rerr != nil {
				return nil
			}
		}
	}
	if !st.IsDir() {
		err = grepFile(in.Path, target)
	} else {
		err = walkOn(t.WS, ctx, target, func(rel string, d fs.DirEntry) error {
			if d.IsDir() || (fileRe != nil && !fileRe.MatchString(rel)) {
				return nil
			}
			return grepFile(rel, filepath.Join(target, rel))
		})
	}
	if err != nil && err != errLimit {
		return "", err
	}
	if longLines > maxLongNotes {
		fmt.Fprintf(&long, "(%d more lines longer than %d MiB were searched only in part)\n", longLines-maxLongNotes, maxScanLine>>20)
	}
	if count == 0 {
		if long.Len() > 0 {
			return "No matches.\n" + long.String(), nil
		}
		return "No matches.", nil
	}
	if err == errLimit {
		fmt.Fprintf(&sb, "(stopped at %d matches; narrow the pattern)\n", maxGrepResults)
	}
	sb.WriteString(long.String())
	return textfmt.Truncate(sb.String()), nil
}

var errLimit = fmt.Errorf("limit reached")

// readLineCapped reads one line from r without its "\n" or "\r\n", the
// way bufio.ScanLines splits, keeping at most max bytes of it and
// discarding the rest; clipped says whether any was discarded. err is
// the read error that ended the line (io.EOF for a last line with no
// newline), with whatever of the line was read before it. The line is
// appended to line[:0], so a caller reading many lines passes the last
// one back and the buffer is reused rather than allocated per line.
func readLineCapped(r *bufio.Reader, max int, line []byte) (_ []byte, clipped bool, err error) {
	line = line[:0]
	for {
		frag, e := r.ReadSlice('\n')
		if e == nil {
			frag = frag[:len(frag)-1] // the newline ends the line; it is not content
		}
		if room := max - len(line); len(frag) > room {
			line = append(line, frag[:room]...)
			clipped = true
		} else {
			line = append(line, frag...)
		}
		if e == bufio.ErrBufferFull {
			continue
		}
		return bytes.TrimSuffix(line, []byte("\r")), clipped, e
	}
}
