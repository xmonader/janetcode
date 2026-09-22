// Listing saved sessions: what /sessions and --continue read. The store
// itself is in session.go; nothing here writes.
//
// There is deliberately no index or metadata sidecar. One was considered:
// the listing would then not have to read the conversations at all. But
// every session file is appended to constantly -- a turn writes dozens of
// events -- so an index would be invalidated by nearly every write, would
// need its own atomic write and its own lock against the sessions of
// other running processes, and would hold a second copy of facts that are
// already on the first line of each file. The cost it would save is
// mostly not the fold: it is parsing sessions belonging to other
// projects, and those are now rejected on their metadata line (see the
// want filter in open). What is left is one parse per session of the
// project being listed, which is the data the listing actually reports.
package session

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// sessionIDs lists the session ids in dir, newest first. The id begins
// with a UTC timestamp, so the reverse lexical order is chronological.
func sessionIDs(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".jsonl") {
			names = append(names, strings.TrimSuffix(e.Name(), ".jsonl"))
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	return names, nil
}

// inCwd matches the sessions of one project, or every session when cwd
// is "". It is the filter both Latest and List hand to the reader, so a
// session belonging to another project costs one line rather than a
// whole parse and fold.
func inCwd(cwd string) func(Meta) bool {
	if cwd == "" {
		return nil
	}
	return func(m Meta) bool { return m.Cwd == cwd }
}

// Latest returns the most recent session ID recorded for cwd, or "" if none.
func Latest(dir, cwd string) (string, error) {
	names, err := sessionIDs(dir)
	if err != nil {
		return "", err
	}
	want := inCwd(cwd)
	// The newest session with a conversation in it. A newer one that
	// never got past its first prompt (a goal set and abandoned, a start
	// that was quit) would otherwise be what --continue lands in, with
	// nothing to continue. One pass: the fallbacks are remembered as the
	// files go by, rather than read again from the top, which on a
	// long-lived sessions directory meant parsing every log twice.
	empty := ""
	for _, id := range names {
		_, msgs, matched, err := open(filepath.Join(dir, id+".jsonl"), id, want)
		if err != nil || !matched {
			continue
		}
		if len(msgs) > 0 {
			return id, nil
		}
		if empty == "" {
			empty = id
		}
	}
	return empty, nil
}

// Info summarises a stored session.
type Info struct {
	Meta     Meta
	Messages int
	Preview  string    // first user text, trimmed
	Updated  time.Time // last activity, from the file
}

// Label is the title when set, else the preview, else the id.
func (in Info) Label() string {
	switch {
	case CleanTitle(in.Meta.Title) != "":
		return CleanTitle(in.Meta.Title)
	case in.Preview != "":
		return in.Preview
	}
	return in.Meta.ID
}

// List returns the sessions recorded for cwd (all when cwd is ""), newest
// first. Unreadable files are skipped.
func List(dir, cwd string) ([]Info, error) {
	names, err := sessionIDs(dir)
	if err != nil {
		return nil, err
	}
	want := inCwd(cwd)
	var out []Info
	for _, id := range names {
		path := filepath.Join(dir, id+".jsonl")
		s, msgs, matched, err := open(path, id, want)
		if err != nil || !matched {
			continue
		}
		info := Info{Meta: s.Meta, Messages: len(msgs), Updated: s.Meta.Created}
		if st, err := os.Stat(path); err == nil {
			info.Updated = st.ModTime()
		}
		info.Preview = preview(msgs)
		out = append(out, info)
	}
	// Active sessions first, most recently touched first; settled ones
	// after them. A session someone comes back to un-settles itself and
	// rises again.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Meta.Settled != out[j].Meta.Settled {
			return !out[i].Meta.Settled
		}
		if !out[i].Updated.Equal(out[j].Updated) {
			return out[i].Updated.After(out[j].Updated)
		}
		return newer(out[i].Meta, out[j].Meta)
	})
	return out, nil
}

// preview is the line a listing shows for a session: the first thing the
// user actually typed. Falling back to a synthetic message is better than
// showing the bare id, but only as a fallback -- naming a session after a
// goal round or an undo note gave lists full of rows that all read the
// same.
func preview(msgs []llm.Message) string {
	fallback := ""
	for _, m := range msgs {
		if m.Role != llm.RoleUser {
			continue
		}
		t := strings.TrimSpace(m.Text())
		if t == "" {
			continue
		}
		if !Synthetic(t) {
			return textfmt.FirstLine(t, 60)
		}
		if fallback == "" {
			fallback = textfmt.FirstLine(t, 60)
		}
	}
	return fallback
}

// newer orders sessions newest first; ids made in the same second differ
// only by their random suffix, so the creation time decides.
func newer(a, b Meta) bool {
	if !a.Created.Equal(b.Created) {
		return a.Created.After(b.Created)
	}
	return a.ID > b.ID
}
