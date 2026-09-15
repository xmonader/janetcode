// Listing saved sessions: what /sessions and --continue read. The store
// itself is in session.go; nothing here writes.
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

// Latest returns the most recent session ID recorded for cwd, or "" if none.
func Latest(dir, cwd string) (string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".jsonl") {
			names = append(names, strings.TrimSuffix(e.Name(), ".jsonl"))
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	// The newest session with a conversation in it. A newer one that
	// never got past its first prompt (a goal set and abandoned, a start
	// that was quit) would otherwise be what --continue lands in, with
	// nothing to continue.
	empty := ""
	for _, id := range names {
		s, msgs, err := Open(dir, id)
		if err != nil || s.Meta.Cwd != cwd {
			continue
		}
		if len(msgs) > 0 {
			return id, nil
		}
		if empty == "" {
			empty = id
		}
	}
	if empty != "" {
		return empty, nil
	}
	for _, id := range names {
		s, _, err := Open(dir, id)
		if err != nil {
			continue
		}
		if s.Meta.Cwd == cwd {
			return id, nil
		}
	}
	return "", nil
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
	case in.Meta.Title != "":
		return in.Meta.Title
	case in.Preview != "":
		return in.Preview
	}
	return in.Meta.ID
}

// List returns the sessions recorded for cwd (all when cwd is ""), newest
// first. Unreadable files are skipped.
func List(dir, cwd string) ([]Info, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Info
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		s, msgs, err := Open(dir, strings.TrimSuffix(e.Name(), ".jsonl"))
		if err != nil || (cwd != "" && s.Meta.Cwd != cwd) {
			continue
		}
		info := Info{Meta: s.Meta, Messages: len(msgs), Updated: s.Meta.Created}
		if st, err := os.Stat(filepath.Join(dir, e.Name())); err == nil {
			info.Updated = st.ModTime()
		}
		for _, m := range msgs {
			if m.Role == llm.RoleUser {
				if t := strings.TrimSpace(m.Text()); t != "" && !Synthetic(t) {
					info.Preview = textfmt.FirstLine(t, 60)
					break
				}
			}
		}
		if info.Preview == "" {
			// Nothing but machinery in it: better the first line of that
			// than the bare id.
			for _, m := range msgs {
				if m.Role == llm.RoleUser {
					if t := strings.TrimSpace(m.Text()); t != "" {
						info.Preview = textfmt.FirstLine(t, 60)
						break
					}
				}
			}
		}
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

// newer orders sessions newest first; ids made in the same second differ
// only by their random suffix, so the creation time decides.
func newer(a, b Meta) bool {
	if !a.Created.Equal(b.Created) {
		return a.Created.After(b.Created)
	}
	return a.ID > b.ID
}
