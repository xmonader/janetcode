package tool

import (
	"fmt"
	"strings"
	"sync"
)

// A model that missed an edit once usually misses it again the same way:
// it re-quotes the text from memory with one more tweak, and each retry
// costs a round trip that teaches it nothing new. The bare "not found"
// leaves it guessing between three different causes — the edit already
// landed, the quote is slightly off, or the text it remembers is no longer
// there — and the right move differs for each. Everything here is plain
// string work on the file already in hand; no model is asked anything.

// editMiss is the failure of an edit whose old_string is not in the file.
// It carries what the hints need, and reads as the plain message when the
// escalation is off.
type editMiss struct {
	msg, old, new, content string
	// hadNear is set when the message already quotes the closest region,
	// so the closest-line hint is not said twice.
	hadNear bool
	// path and display name the file; plan fills them in.
	path, display string
}

func (m *editMiss) Error() string { return m.msg }

// EditMisses counts consecutive failed edits per file. A landed edit
// to the file clears its count: the model has found its footing again.
type EditMisses struct {
	mu sync.Mutex
	n  map[string]int
}

// NewEditMisses returns an empty counter.
func NewEditMisses() *EditMisses { return &EditMisses{n: map[string]int{}} }

// NewEditMissesIf is NewEditMisses unless off, when it is nil.
func NewEditMissesIf(off bool) *EditMisses {
	if off {
		return nil
	}
	return NewEditMisses()
}

// miss records one more failure on path and returns how many in a row.
func (m *EditMisses) miss(path string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.n[path]++
	return m.n[path]
}

// landed clears the count for path. A nil counter is a no-op.
func (m *EditMisses) landed(path string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.n, path)
}

// escalate adds the hints and the repeat-miss advice to a miss, leaving
// any other error, and every error when the counter is off, untouched.
func (t EditFile) escalate(err error) error {
	miss, ok := err.(*editMiss)
	if t.Misses == nil || !ok {
		return err
	}
	msg := miss.msg
	switch {
	case miss.new != "" && strings.Contains(miss.content, miss.new):
		msg += "\nthe replacement already appears in the file — this edit may already be applied; re-read before retrying"
	case !miss.hadNear:
		if n, text, found := closestLine(miss.content, miss.old); found {
			msg += fmt.Sprintf("\nclosest line is %d: %s; copy the exact text from the file", n, text)
		} else {
			msg += "\nno similar line was found in the file; re-read it before retrying"
		}
	}
	if t.Misses.miss(miss.path) >= 2 {
		msg += fmt.Sprintf("\nThis is the second failed edit on %s. Stop adjusting the quoted text: re-read the file, then replace the whole enclosing function with ", miss.display)
		if t.HaveAst {
			msg += "ast_edit (or one edit covering the whole function)."
		} else {
			msg += "one edit covering the whole function."
		}
	}
	return fmt.Errorf("%s", msg)
}

// closestLine finds the line of content most like the first non-blank line
// of old, by the overlap of their word sets, and reports it only when more
// than half the words are shared: below that a "closest" line is noise.
func closestLine(content, old string) (int, string, bool) {
	want := ""
	for _, l := range strings.Split(old, "\n") {
		if strings.TrimSpace(l) != "" {
			want = l
			break
		}
	}
	ws := wordSet(want)
	if len(ws) == 0 {
		return 0, "", false
	}
	best, bestAt, bestLine := 0.5, 0, ""
	for i, l := range strings.Split(content, "\n") {
		if j := jaccard(ws, wordSet(l)); j > best {
			best, bestAt, bestLine = j, i+1, strings.TrimSpace(l)
		}
	}
	return bestAt, bestLine, bestAt > 0
}

func wordSet(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.FieldsFunc(s, func(r rune) bool {
		return !(r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r > 127)
	}) {
		out[w] = true
	}
	return out
}

func jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	in := 0
	for w := range a {
		if b[w] {
			in++
		}
	}
	return float64(in) / float64(len(a)+len(b)-in)
}
