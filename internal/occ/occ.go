// Package occ is optimistic concurrency for editing subagents: each one
// works in its own checkout of the project, and when it finishes its
// changes are merged back into a project that other subagents (or the
// main agent) may have changed in the meantime.
//
// Nothing is locked while a subagent works. At merge-back the harness
// asks two questions, both deterministically and with no model call:
//
//   - Do the edits commute? A file both sides changed is merged when the
//     two sets of hunks touch disjoint regions of the base — disjoint
//     declarations when the file parses (go/parser for Go, a tree-sitter
//     grammar for what one is compiled in for), disjoint and
//     non-adjacent line ranges when it does not.
//   - Is the read-set still valid? A file the subagent read, or a grep or
//     glob it ran, may have changed under it. A changed file matters when
//     a declaration the other side changed is named in the subagent's own
//     edits; a grep or glob matters when its answer would now differ.
//
// What does not commute is not aborted. It is described — the conflicting
// delta from both sides — so the subagent can be sent back once to
// re-judge it against the current project ("runtime informs, agent
// repairs"). This package only answers; internal/agent does the writing
// and the asking.
package occ

import (
	"bytes"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/xmonader/janetcode/internal/diff"
	"github.com/xmonader/janetcode/internal/tool"
)

// Version is one side's content of a file: Data, or no file at all.
type Version struct {
	Data   []byte
	Exists bool
}

// Same reports whether two versions are the same file content.
func (v Version) Same(o Version) bool {
	return v.Exists == o.Exists && (!v.Exists || bytes.Equal(v.Data, o.Data))
}

// File is one path seen three ways: as the subagent started from it
// (Base), as the subagent left it (Ours), and as the project holds it now
// (Theirs). Path is relative and slash-separated.
type File struct {
	Path   string
	Base   Version
	Ours   Version
	Theirs Version
}

// PatternRead is a grep or glob the subagent ran: a range read, whose
// answer can change without any file it named changing — a new file can
// start to match.
type PatternRead struct {
	Tool            string // "grep" or "glob"
	Pattern         string
	Path            string // the directory or file searched, relative; "" or "." is the root
	Glob            string // grep's file filter
	CaseInsensitive bool
}

// Input is everything Check looks at.
type Input struct {
	// Written are the files the subagent changed (Ours differs from Base).
	Written []File
	// Read are the files the subagent read and did not change; Ours is
	// unused.
	Read []File
	// Moved are the files the project changed since the base (Theirs
	// differs from Base), which is what a grep or glob is checked over.
	Moved    []File
	Patterns []PatternRead
}

// Kind says why part of the subagent's work does not merge on its own.
type Kind string

const (
	// Overlap: both sides edited the same region of one file.
	Overlap Kind = "overlap"
	// Opaque: both sides changed a file that cannot be merged by lines —
	// one side deleted it, or it is not text.
	Opaque Kind = "opaque"
	// StaleRead: a file the subagent read changed, in a declaration its
	// own edits name.
	StaleRead Kind = "stale-read"
	// StalePattern: a grep or glob the subagent ran would answer
	// differently now.
	StalePattern Kind = "stale-pattern"
)

// Conflict is one part of the subagent's work the harness could not
// settle. Ours and Theirs are unified diffs against the base, for the
// message that hands it back.
type Conflict struct {
	Kind   Kind
	Path   string
	Detail string
	Ours   string
	Theirs string
}

// Result is what Check decided.
type Result struct {
	// Apply is the content each written path should get in the project:
	// Ours where the project had not touched the file, the merge of both
	// where it had and the edits commute. A path with Exists false is
	// deleted. Paths in conflict are not in it.
	Apply map[string]Version
	// Clean and Merged name the paths in Apply: taken as the subagent
	// left them, or merged with a concurrent change.
	Clean, Merged []string
	// Independent are files the subagent read that changed under it in
	// declarations its edits do not name: reported, not sent back.
	Independent []string
	Conflicts   []Conflict
}

// OK reports whether everything merged.
func (r Result) OK() bool { return len(r.Conflicts) == 0 }

// Check validates a subagent's work against the project as it is now.
func Check(in Input) Result {
	res := Result{Apply: map[string]Version{}}
	// The identifiers the subagent's own edits mention: a stale read
	// matters when the other side changed something these name.
	uses := map[string]bool{}
	for _, f := range in.Written {
		for _, id := range changedIdents(f.Base, f.Ours) {
			uses[id] = true
		}
	}
	for _, f := range in.Written {
		switch {
		case f.Theirs.Same(f.Base):
			res.Apply[f.Path] = f.Ours
			res.Clean = append(res.Clean, f.Path)
		case f.Theirs.Same(f.Ours):
			// Both made the same change: nothing left to apply, and
			// nothing to report as a conflict.
			res.Clean = append(res.Clean, f.Path)
		case !f.Base.Exists || !f.Ours.Exists || !f.Theirs.Exists || binary(f.Base.Data) || binary(f.Ours.Data) || binary(f.Theirs.Data):
			res.Conflicts = append(res.Conflicts, Conflict{Kind: Opaque, Path: f.Path,
				Detail: "both sides changed it and one of them created, deleted or binary-edited it, which does not merge by lines",
				Ours:   unified(f.Path, f.Base, f.Ours), Theirs: unified(f.Path, f.Base, f.Theirs)})
		default:
			merged, clash := Merge(f.Path, f.Base.Data, f.Ours.Data, f.Theirs.Data)
			if clash != "" {
				res.Conflicts = append(res.Conflicts, Conflict{Kind: Overlap, Path: f.Path, Detail: clash,
					Ours: unified(f.Path, f.Base, f.Ours), Theirs: unified(f.Path, f.Base, f.Theirs)})
				continue
			}
			res.Apply[f.Path] = Version{Data: merged, Exists: true}
			res.Merged = append(res.Merged, f.Path)
		}
	}
	for _, f := range in.Read {
		if f.Theirs.Same(f.Base) {
			continue
		}
		names, known := touchedNames(f.Path, f.Base, f.Theirs)
		var hit []string
		for _, n := range names {
			if uses[n] {
				hit = append(hit, n)
			}
		}
		switch {
		case !known:
			// No structure to judge by: a read file that moved is assumed
			// to matter, because guessing it does not is the silent
			// failure this whole check exists to stop.
			res.Conflicts = append(res.Conflicts, Conflict{Kind: StaleRead, Path: f.Path,
				Detail: "you read it and it changed since; it does not parse into declarations, so every change to it is treated as one your edits may depend on",
				Theirs: unified(f.Path, f.Base, f.Theirs)})
		case len(hit) > 0:
			res.Conflicts = append(res.Conflicts, Conflict{Kind: StaleRead, Path: f.Path,
				Detail: "you read it and it changed since, in " + strings.Join(hit, ", ") + ", which your own edits use",
				Theirs: unified(f.Path, f.Base, f.Theirs)})
		default:
			res.Independent = append(res.Independent, f.Path)
		}
	}
	for _, p := range in.Patterns {
		if why := patternMoved(p, in.Moved); why != "" {
			res.Conflicts = append(res.Conflicts, Conflict{Kind: StalePattern, Path: p.Path, Detail: why})
		}
	}
	sort.Strings(res.Clean)
	sort.Strings(res.Merged)
	sort.Strings(res.Independent)
	return res
}

// patternMoved says how a grep or glob would answer differently over the
// files the project changed, or "" when it would not.
func patternMoved(p PatternRead, moved []File) string {
	scope := strings.Trim(path.Clean("/"+p.Path), "/")
	inScope := func(rel string) (string, bool) {
		if scope == "" {
			return rel, true
		}
		if rel == scope {
			return path.Base(rel), true
		}
		if r, ok := strings.CutPrefix(rel, scope+"/"); ok {
			return r, true
		}
		return "", false
	}
	switch p.Tool {
	case "glob":
		re, err := tool.GlobRegexp(p.Pattern)
		if err != nil {
			return ""
		}
		var gained, lost []string
		for _, f := range moved {
			rel, ok := inScope(f.Path)
			if !ok || f.Base.Exists == f.Theirs.Exists || !re.MatchString(rel) {
				continue
			}
			if f.Theirs.Exists {
				gained = append(gained, f.Path)
			} else {
				lost = append(lost, f.Path)
			}
		}
		return answerMoved("glob "+p.Pattern, gained, lost)
	case "grep":
		expr := p.Pattern
		if p.CaseInsensitive {
			expr = "(?i)" + expr
		}
		re, err := regexp.Compile(expr)
		if err != nil {
			return ""
		}
		var fileRe *regexp.Regexp
		if p.Glob != "" {
			g := p.Glob
			if !strings.Contains(g, "/") {
				g = "**/" + g
			}
			if fileRe, err = tool.GlobRegexp(g); err != nil {
				return ""
			}
		}
		var gained, lost []string
		for _, f := range moved {
			rel, ok := inScope(f.Path)
			if !ok || (fileRe != nil && !fileRe.MatchString(rel)) {
				continue
			}
			before, after := matching(re, f.Base), matching(re, f.Theirs)
			if slicesEqual(before, after) {
				continue
			}
			if len(after) > len(before) {
				gained = append(gained, f.Path)
			} else {
				lost = append(lost, f.Path)
			}
		}
		return answerMoved("grep "+p.Pattern, gained, lost)
	}
	return ""
}

func answerMoved(what string, gained, lost []string) string {
	if len(gained) == 0 && len(lost) == 0 {
		return ""
	}
	var parts []string
	if len(gained) > 0 {
		parts = append(parts, "now also matches in "+strings.Join(gained, ", "))
	}
	if len(lost) > 0 {
		parts = append(parts, "matches differently or no longer in "+strings.Join(lost, ", "))
	}
	return fmt.Sprintf("you ran %s and its answer has changed since: it %s", what, strings.Join(parts, "; "))
}

// matching is the lines of v that re matches, in order.
func matching(re *regexp.Regexp, v Version) []string {
	if !v.Exists || binary(v.Data) {
		return nil
	}
	var out []string
	for _, l := range strings.Split(string(v.Data), "\n") {
		if re.MatchString(l) {
			out = append(out, l)
		}
	}
	return out
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// binary is the NUL-in-the-first-8KB test git and the read tools use.
func binary(b []byte) bool {
	return bytes.IndexByte(b[:min(len(b), 8000)], 0) >= 0
}

// unified renders base→v as a diff for a message; "" when nothing changed.
func unified(name string, base, v Version) string {
	if base.Same(v) {
		return ""
	}
	if !v.Exists {
		return "(deleted " + name + ")"
	}
	if binary(v.Data) || binary(base.Data) {
		return "(binary change to " + name + ")"
	}
	return diff.Unified(name, string(base.Data), string(v.Data), 3)
}

var identRe = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)

// changedIdents is every identifier on a line the edit added or removed.
func changedIdents(base, v Version) []string {
	if !v.Exists || binary(v.Data) {
		return nil
	}
	var b []string
	if base.Exists && !binary(base.Data) {
		b = diff.SplitLinesKeepEOL(string(base.Data))
	}
	var out []string
	for _, op := range diff.Lines(b, diff.SplitLinesKeepEOL(string(v.Data))) {
		if op.Kind != ' ' {
			out = append(out, identRe.FindAllString(op.Text, -1)...)
		}
	}
	return out
}
