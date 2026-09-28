// Package plan holds the plan file and the comments the user leaves on
// it: the part of plan mode that is neither the terminal's nor the web
// UI's, so that both front-ends only parse a command and render what
// comes back.
//
// The file is the plan. A plan written into a reply scrolls away, and a
// plan the harness also kept in a field would be a second copy free to
// drift from the one the user is reading. So the turn that ends with a
// plan writes it to <root>/.janetcode/plan.md — the same place every
// other per-project file lives (hooks.json, verify.json, commands/) —
// and everything afterwards reads that file back. Nothing here caches
// its text.
//
// Comments are the exception, and they are not a copy of anything: a
// line number and a sentence the user typed, held until they send them
// or approve. They live in Comments, which the session owns.
//
// It is a leaf package (standard library only) so the agent, the
// terminal and the web UI can all use it.
package plan

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// Name is the plan file's path inside a project, and PrevName the copy
// of the version before the last revision. One previous version is kept
// because it costs a rename and answers the only question a revision
// raises — what changed — with an ordinary diff; a numbered history
// would be a directory nobody prunes.
const (
	Name     = ".janetcode/plan.md"
	PrevName = ".janetcode/plan.md.prev"
)

// File is the plan file for the project rooted at root.
func File(root string) string { return filepath.Join(root, ".janetcode", "plan.md") }

// PrevFile is the previous version of that plan.
func PrevFile(root string) string { return File(root) + ".prev" }

// Extract pulls the plan out of an assistant message: everything from
// the `## Plan` heading the plan-mode prompt asks for to the end of the
// message. A reply with no such heading is not a plan — it is a
// question, or a note on the way to one — and returns ok false, so a
// plan file is never written from chatter.
func Extract(text string) (body string, ok bool) {
	lines := strings.Split(text, "\n")
	fence := "" // the open code fence's marker, "" outside one
	for i, line := range lines {
		t := strings.TrimSpace(line)
		// A heading inside a code fence is quoted text — a doc's own
		// "## Plan", a shell comment — not the reply's heading.
		if fence != "" {
			if strings.HasPrefix(t, fence) && strings.Trim(t, fence[:1]) == "" {
				fence = ""
			}
			continue
		}
		if m := fenceMarker(t); m != "" {
			fence = m
			continue
		}
		if !strings.HasPrefix(t, "#") {
			continue
		}
		title := strings.ToLower(strings.TrimSpace(strings.TrimLeft(t, "#")))
		if level := len(t) - len(strings.TrimLeft(t, "#")); level > 3 {
			continue
		}
		if title != "plan" && !strings.HasPrefix(title, "plan:") && !strings.HasPrefix(title, "plan ") {
			continue
		}
		body = strings.TrimRight(strings.Join(lines[i:], "\n"), " \t\n")
		return body, strings.TrimSpace(body) != ""
	}
	return "", false
}

// fenceMarker is the run of backticks or tildes that opens a code fence
// on line t, or "".
func fenceMarker(t string) string {
	for _, c := range []string{"`", "~"} {
		if n := len(t) - len(strings.TrimLeft(t, c)); n >= 3 {
			return strings.Repeat(c, n)
		}
	}
	return ""
}

// ErrNoRoot is returned by Write when there is no project root to put the
// plan file in.
var ErrNoRoot = errors.New("plan: no project root")

// Write puts body in the project's plan file, moving the version it
// replaces to plan.md.prev. It reports whether the file changed: a turn
// that restates the same plan rewrites nothing and keeps the previous
// version pointing at the last real revision.
func Write(root, body string) (changed bool, err error) {
	// Without a project root the path would be relative to whatever
	// directory the process happens to be in — which is how a test once
	// wrote a plan into the package it was testing. A session always has
	// a root (Agent.Root); anything else keeps its plan in memory only.
	if root == "" {
		return false, ErrNoRoot
	}
	path := File(root)
	body = strings.TrimRight(body, "\n") + "\n"
	if old, err := os.ReadFile(path); err == nil {
		if string(old) == body {
			return false, nil
		}
		if err := os.WriteFile(PrevFile(root), old, 0o644); err != nil {
			return false, err
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return false, err
	}
	return true, nil
}

// Read returns the plan as written, without the trailing newline.
func Read(root string) (string, error) {
	data, err := os.ReadFile(File(root))
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(data), "\n"), nil
}

// Lines is the plan split the way a comment addresses it: line 1 is the
// first line of the file.
func Lines(body string) []string { return strings.Split(body, "\n") }

// Numbered renders the plan with line numbers, in the same shape /open
// gives a file, so a comment can name a line the user can see. Lines
// carrying a comment are marked.
func Numbered(body string, comments []Comment) string {
	lines := Lines(body)
	byLine := map[int][]string{}
	for _, c := range comments {
		byLine[c.Line] = append(byLine[c.Line], c.Text)
	}
	w := len(strconv.Itoa(len(lines)))
	var sb strings.Builder
	for i, line := range lines {
		n := i + 1
		mark := " "
		if len(byLine[n]) > 0 {
			mark = "*"
		}
		fmt.Fprintf(&sb, "%s%*d │ %s\n", mark, w, n, strings.ReplaceAll(line, "\t", "    "))
		for _, c := range byLine[n] {
			fmt.Fprintf(&sb, "%s%*s │ ↳ %s\n", " ", w, "", c)
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

// Comment is one remark against one line of the plan.
type Comment struct {
	Line int
	Text string
}

// Comments is the set a session has collected and not yet sent. It is
// held by the session rather than written down: a comment is worth
// something until it reaches the model, and nothing afterwards.
type Comments struct {
	mu   sync.Mutex
	list []Comment
}

// Add records a comment on a line.
func (c *Comments) Add(line int, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.list = append(c.list, Comment{Line: line, Text: text})
}

// List is what has been collected, in the order it was typed.
func (c *Comments) List() []Comment {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Comment(nil), c.list...)
}

// Len is how many are waiting.
func (c *Comments) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.list)
}

// Take returns the collected comments and empties the collection.
func (c *Comments) Take() []Comment {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.list
	c.list = nil
	return out
}

// ParseRef reads the line a comment is attached to. Both `12` and the
// `plan.md:12` a review line is prefilled with are accepted, and the
// rest of the argument is the comment.
func ParseRef(arg string) (line int, text string, ok bool) {
	arg = strings.TrimSpace(arg)
	ref, rest, _ := strings.Cut(arg, " ")
	if i := strings.LastIndexByte(ref, ':'); i >= 0 {
		ref = ref[i+1:]
	}
	n, err := strconv.Atoi(strings.TrimSpace(ref))
	if err != nil || n < 1 {
		return 0, "", false
	}
	return n, strings.TrimSpace(rest), true
}

// Message is the one message the collected comments become: each
// comment with the plan line it is about, so the model can see what was
// meant without being sent the plan again — it wrote it, and the file is
// on disk.
func Message(body string, comments []Comment) string {
	lines := Lines(body)
	var sb strings.Builder
	sb.WriteString("I reviewed the plan in " + Name + " and left comments on it. Address every one, then rewrite the whole plan under a `## Plan` heading. Stay in plan mode: do not start making changes.\n")
	for _, c := range comments {
		sb.WriteString("\n" + Name + ":" + strconv.Itoa(c.Line))
		if c.Line >= 1 && c.Line <= len(lines) {
			if t := strings.TrimSpace(lines[c.Line-1]); t != "" {
				sb.WriteString("  " + t)
			}
		}
		sb.WriteString("\n  " + c.Text + "\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}
