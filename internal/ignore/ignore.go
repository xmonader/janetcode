// Package ignore answers whether the agent's file tools may touch a path,
// from the ignore files a project keeps for AI tools: .aiignore,
// .agentignore and .janetcodeignore. The syntax is .gitignore's: nested
// files apply to their own subtree, "/" anchors, a trailing "/" means a
// directory, "**" spans directories, "!" re-includes, and an excluded
// directory excludes everything beneath it.
//
// .gitignore itself is not consulted. It says what not to commit, not
// what the agent may not read: build output and vendored code are often
// exactly what a question is about.
package ignore

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Files are the ignore files, in the order their rules apply: a later
// file's rule wins over an earlier one's in the same directory.
var Files = []string{".aiignore", ".agentignore", ".janetcodeignore"}

// foldCase makes patterns match regardless of case: macOS and Windows
// file systems are case-insensitive, so SECRETS.env is secrets.env there.
// A variable so a test can exercise it on any platform.
var foldCase = runtime.GOOS == "darwin" || runtime.GOOS == "windows"

// maxFile caps one ignore file; anything longer is not a hand-kept list.
const maxFile = 1 << 20

type rule struct {
	re      *regexp.Regexp
	negate  bool
	dirOnly bool
	src     string // file:line, for the message
}

type cached struct {
	stamp   string // the files' sizes and mtimes when parsed
	checked time.Time
	rules   []rule
}

// fresh is how long a directory's rules are trusted without looking at
// its ignore files again: a walk of ten thousand files would otherwise
// stat three files per ancestor of every one of them.
const fresh = time.Second

// Matcher holds the rules under one root. It is safe for concurrent use
// and rereads a directory's ignore files when they change.
type Matcher struct {
	root string
	mu   sync.Mutex
	dirs map[string]cached // by slash path relative to root ("" is root)
}

var (
	matchersMu sync.Mutex
	matchers   = map[string]*Matcher{}
)

// For is the shared matcher for root.
func For(root string) *Matcher {
	root = filepath.Clean(root)
	matchersMu.Lock()
	defer matchersMu.Unlock()
	m := matchers[root]
	if m == nil {
		m = &Matcher{root: root, dirs: map[string]cached{}}
		matchers[root] = m
	}
	return m
}

// Excluded reports whether abs, a path under the root, is excluded, and
// by which rule ("" when it is not). isDir says whether abs is a
// directory; a directory-only pattern matches nothing else.
func (m *Matcher) Excluded(abs string, isDir bool) (bool, string) {
	rel, err := filepath.Rel(m.root, abs)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false, ""
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	// An excluded ancestor excludes everything under it, and nothing
	// under it can be re-included: git does not look inside it either.
	for i := 1; i <= len(parts); i++ {
		dir := i < len(parts) || isDir
		if hit, src := m.match(parts[:i], dir); hit {
			return true, src
		}
	}
	return false, ""
}

// match applies the rules of every ignore file from the root down to the
// path's parent; the last rule that matches decides.
func (m *Matcher) match(parts []string, isDir bool) (bool, string) {
	excluded, src := false, ""
	for depth := 0; depth < len(parts); depth++ {
		base := strings.Join(parts[:depth], "/")
		rel := strings.Join(parts[depth:], "/")
		for _, r := range m.rules(base) {
			if r.dirOnly && !isDir {
				continue
			}
			if r.re.MatchString(rel) {
				excluded, src = !r.negate, r.src
			}
		}
	}
	if !excluded {
		src = ""
	}
	return excluded, src
}

// rules are dir's own, parsed once per change to its ignore files.
func (m *Matcher) rules(dir string) []rule {
	m.mu.Lock()
	if c, ok := m.dirs[dir]; ok && time.Since(c.checked) < fresh {
		m.mu.Unlock()
		return c.rules
	}
	m.mu.Unlock()
	abs := filepath.Join(m.root, filepath.FromSlash(dir))
	var stamp strings.Builder
	for _, f := range Files {
		if fi, err := os.Stat(filepath.Join(abs, f)); err == nil && fi.Mode().IsRegular() {
			stamp.WriteString(f + ":" + fi.ModTime().Format(time.RFC3339Nano) + ":" + itoa(fi.Size()) + ";")
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.dirs[dir]; ok && c.stamp == stamp.String() {
		c.checked = time.Now()
		m.dirs[dir] = c
		return c.rules
	}
	var rs []rule
	if stamp.Len() > 0 {
		for _, f := range Files {
			rs = append(rs, parseFile(filepath.Join(abs, f), path(dir, f))...)
		}
	}
	m.dirs[dir] = cached{stamp: stamp.String(), checked: time.Now(), rules: rs}
	return rs
}

func path(dir, f string) string {
	if dir == "" {
		return f
	}
	return dir + "/" + f
}

func itoa(n int64) string {
	var b [20]byte
	i := len(b)
	if n == 0 {
		return "0"
	}
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func parseFile(abs, name string) []rule {
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil
	}
	if len(data) > maxFile {
		data = data[:maxFile]
	}
	var out []rule
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64*1024), maxFile)
	n := 0
	for sc.Scan() {
		n++
		if r, ok := Parse(sc.Text()); ok {
			r.src = name + ":" + itoa(int64(n))
			out = append(out, r)
		}
	}
	return out
}

// Parse turns one gitignore line into a rule; ok is false for a blank
// line, a comment, or a pattern that cannot be compiled.
func Parse(line string) (rule, bool) {
	line = strings.TrimSuffix(line, "\r")
	// Trailing spaces are dropped unless escaped.
	for strings.HasSuffix(line, " ") && !strings.HasSuffix(line, "\\ ") {
		line = line[:len(line)-1]
	}
	if line == "" || strings.HasPrefix(line, "#") {
		return rule{}, false
	}
	var r rule
	if strings.HasPrefix(line, "!") {
		r.negate = true
		line = line[1:]
	} else if strings.HasPrefix(line, `\!`) || strings.HasPrefix(line, `\#`) {
		line = line[1:]
	}
	if strings.HasSuffix(line, "/") {
		r.dirOnly = true
		line = strings.TrimRight(line, "/")
	}
	if line == "" {
		return rule{}, false
	}
	// A slash anywhere but the end anchors the pattern to the file's
	// directory; otherwise it matches a name at any depth.
	anchored := strings.Contains(line, "/")
	line = strings.TrimPrefix(line, "/")
	expr := globToRegexp(line)
	if anchored {
		expr = "^" + expr + "$"
	} else {
		expr = "^(?:.*/)?" + expr + "$"
	}
	if foldCase {
		expr = "(?i)" + expr
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return rule{}, false
	}
	r.re = re
	return r, true
}

func globToRegexp(g string) string {
	var sb strings.Builder
	for i := 0; i < len(g); i++ {
		c := g[i]
		switch {
		case strings.HasPrefix(g[i:], "**/"):
			sb.WriteString("(?:.*/)?")
			i += 2
		case strings.HasPrefix(g[i:], "/**") && i+3 == len(g):
			sb.WriteString("/.*")
			i += 2
		case strings.HasPrefix(g[i:], "**"):
			sb.WriteString(".*")
			i++
		case c == '*':
			sb.WriteString("[^/]*")
		case c == '?':
			sb.WriteString("[^/]")
		case c == '\\' && i+1 < len(g):
			i++
			sb.WriteString(regexp.QuoteMeta(string(g[i])))
		case c == '[':
			end := strings.IndexByte(g[i+1:], ']')
			if end < 0 {
				sb.WriteString(`\[`)
				continue
			}
			class := g[i+1 : i+1+end]
			if strings.HasPrefix(class, "!") {
				class = "^" + class[1:]
			}
			sb.WriteString("[" + strings.ReplaceAll(class, `\`, `\\`) + "]")
			i += end + 1
		default:
			sb.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	return sb.String()
}
