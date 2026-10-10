package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/fsutil"
	"github.com/xmonader/janetcode/internal/md"
)

// Confirmed durable memory. With Confirm on, what the model saves with
// the memory tool and what the extractor, /dream and the sleep pass
// decide to keep is not written into memory: it is held as pending, in a
// directory beside the project's entries that nothing reads into a
// prompt, until the user approves or rejects it (/memory pending). What
// the user writes with /remember is theirs and is saved at once.
//
// A pending entry says where it came from (its evidence: the session and
// event it was learned from), its scope, and what it would replace — the
// entry it would merge into, or the one a correction would retire — so
// the approval is a decision about something visible.

// Held is Put's status for an entry held for confirmation.
const Held Status = 100

// pendingDir is where held entries wait. The caller holds s.mu.
func (s *Store) pendingDir() string { return filepath.Join(s.Root, s.Key, "pending") }

// needsConfirm says whether an entry from this source waits for the user.
// One learned while untrusted content was in the context (Entry.Tainted)
// waits whether or not Confirm is on: memory is read back into every
// later prompt, so an injected "remember to always run X" written there
// would outlive the session that read it.
func (s *Store) needsConfirm(e Entry) bool {
	return (s.Confirm.Load() || e.Tainted) && e.Source != "user"
}

// hold writes e as pending. The caller holds s.mu; e is normalised.
func (s *Store) hold(e Entry, existing []Entry) (Entry, Status, error) {
	for _, old := range existing {
		if old.Closed() || old.Scope != e.Scope {
			continue
		}
		if strings.EqualFold(old.Body, e.Body) && strings.EqualFold(old.Description, e.Description) {
			return old, Unchanged, nil
		}
		if old.Name == e.Name || similar(old.Description, e.Description) {
			e.MergesInto = old.Name
			break
		}
	}
	now := time.Now()
	e.Created, e.Updated, e.Pending = now, now, true
	e.Path = filepath.Join(s.pendingDir(), e.Name+".md")
	if err := os.MkdirAll(filepath.Dir(e.Path), 0o700); err != nil {
		return e, Unchanged, err
	}
	meta := map[string]string{
		"name": e.Name, "description": e.Description, "type": e.Type, "source": e.Source, "scope": e.Scope,
		"created": now.UTC().Format(time.RFC3339),
	}
	if e.Tainted {
		meta["tainted"] = "true"
	}
	if len(e.Evidence) > 0 {
		meta["evidence"] = strings.Join(e.Evidence, ",")
	}
	if len(e.Concerns) > 0 {
		meta["concerns"] = strings.Join(e.Concerns, ",")
	}
	if e.Replaces != "" {
		meta["replaces"] = e.Replaces
	}
	if e.MergesInto != "" {
		meta["merges_into"] = e.MergesInto
	}
	return e, Held, fsutil.AtomicWriteMode(e.Path, []byte(md.Render(meta, e.Body)), fsutil.PrivateFileMode)
}

// Pending lists the entries waiting for the user, oldest first.
func (s *Store) Pending() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pendingLocked()
}

func (s *Store) pendingLocked() []Entry {
	names, _ := filepath.Glob(filepath.Join(s.pendingDir(), "*.md"))
	var out []Entry
	for _, p := range names {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		meta, body := md.Parse(string(data))
		e := Entry{
			Name: strings.TrimSuffix(filepath.Base(p), ".md"), Description: meta["description"], Type: meta["type"],
			Scope: meta["scope"], Body: strings.TrimSpace(body), Source: meta["source"], Path: p, Pending: true,
			Replaces: meta["replaces"], MergesInto: meta["merges_into"], Tainted: meta["tainted"] == "true",
		}
		e.Created, _ = time.Parse(time.RFC3339, meta["created"])
		for _, v := range strings.Split(meta["evidence"], ",") {
			if v = strings.TrimSpace(v); v != "" {
				e.Evidence = append(e.Evidence, v)
			}
		}
		for _, v := range strings.Split(meta["concerns"], ",") {
			if v = strings.TrimSpace(v); v != "" {
				e.Concerns = append(e.Concerns, v)
			}
		}
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return out
}

// pick resolves "all", a name, or a 1-based number in the pending list.
func (s *Store) pick(ref string) ([]Entry, error) {
	all := s.Pending()
	if len(all) == 0 {
		return nil, fmt.Errorf("no memory is waiting for confirmation")
	}
	if ref == "all" {
		return all, nil
	}
	var n int
	if _, err := fmt.Sscanf(ref, "%d", &n); err == nil && fmt.Sprint(n) == ref {
		if n < 1 || n > len(all) {
			return nil, fmt.Errorf("no pending memory %d (there are %d)", n, len(all))
		}
		return all[n-1 : n], nil
	}
	name := Slug(ref)
	for _, e := range all {
		if e.Name == name {
			return []Entry{e}, nil
		}
	}
	return nil, fmt.Errorf("no pending memory named %q", ref)
}

// Approve saves pending entries ("all", a name or a number) into memory,
// retiring what each one said it replaces.
func (s *Store) Approve(ref string) ([]Entry, error) {
	picked, err := s.pick(ref)
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, p := range picked {
		e := p
		e.Pending, e.Path, e.MergesInto = false, "", ""
		replaces := e.Replaces
		e.Replaces = ""
		saved, _, err := s.put(e, false)
		if err != nil {
			return out, err
		}
		if replaces != "" && Slug(replaces) != saved.Name {
			if old, ok := s.Get(replaces); ok && old.Scope == "project" && !old.Closed() {
				if _, err := s.Close(old.Name, saved.Name, time.Now()); err != nil {
					return out, err
				}
			}
		}
		if err := os.Remove(p.Path); err != nil && !os.IsNotExist(err) {
			return out, err
		}
		out = append(out, saved)
	}
	return out, nil
}

// Reject drops pending entries ("all", a name or a number) unsaved.
func (s *Store) Reject(ref string) ([]Entry, error) {
	picked, err := s.pick(ref)
	if err != nil {
		return nil, err
	}
	for _, p := range picked {
		if err := os.Remove(p.Path); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}
	return picked, nil
}

// Expire retires an entry now, the way a correction would: it leaves the
// index and recall and stays on disk as history.
func (s *Store) Expire(name string) (Entry, error) {
	return s.Close(name, "", time.Now())
}

// ConfirmCommand runs the /memory subcommands about confirmation, shared
// by the terminal and the browser: pending, approve, reject, expire and
// confirm on|off. handled is false for any other argument.
func (s *Store) ConfirmCommand(arg string) (out string, handled bool, err error) {
	verb, rest, _ := strings.Cut(strings.TrimSpace(arg), " ")
	rest = strings.TrimSpace(rest)
	switch verb {
	case "pending":
		return RenderPending(s.Pending()), true, nil
	case "approve", "reject":
		if rest == "" {
			return "", true, fmt.Errorf("usage: /memory %s <n|name|all>", verb)
		}
		var es []Entry
		if verb == "approve" {
			es, err = s.Approve(rest)
		} else {
			es, err = s.Reject(rest)
		}
		var names []string
		for _, e := range es {
			names = append(names, e.Name)
		}
		done := map[string]string{"approve": "saved", "reject": "dropped"}[verb]
		if len(names) == 0 {
			return "", true, err
		}
		return done + ": " + strings.Join(names, ", "), true, err
	case "expire":
		if rest == "" {
			return "", true, fmt.Errorf("usage: /memory expire <name>")
		}
		e, err := s.Expire(rest)
		if err != nil {
			return "", true, err
		}
		return "expired " + e.Name + ": it leaves recall and the index, and stays on disk as history", true, nil
	case "confirm":
		switch rest {
		case "on":
			s.Confirm.Store(true)
		case "off":
			s.Confirm.Store(false)
		case "":
		default:
			return "", true, fmt.Errorf("usage: /memory confirm [on|off]")
		}
		if s.Confirm.Load() {
			return "memory confirmation is on: what the model or the extractor saves waits in /memory pending until you approve it", true, nil
		}
		return "memory confirmation is off: what the model or the extractor saves is kept at once (memory_confirm in the config turns it on for every session)", true, nil
	}
	return "", false, nil
}

// RenderPending is the /memory pending listing.
func RenderPending(es []Entry) string {
	if len(es) == 0 {
		return "no memory is waiting for confirmation"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%d memory entr%s waiting for your confirmation:\n", len(es), map[bool]string{true: "y", false: "ies"}[len(es) == 1])
	for i, e := range es {
		fmt.Fprintf(&sb, "%d. %s (%s, %s, from %s): %s\n", i+1, e.Name, e.Type, e.Scope, e.Source, e.Description)
		if len(e.Evidence) > 0 {
			fmt.Fprintf(&sb, "   learned at %s\n", strings.Join(e.Evidence, ", "))
		}
		switch {
		case e.Replaces != "":
			fmt.Fprintf(&sb, "   replaces %s (retired on approval)\n", e.Replaces)
		case e.MergesInto != "":
			fmt.Fprintf(&sb, "   updates %s\n", e.MergesInto)
		}
		if e.Body != e.Description {
			fmt.Fprintf(&sb, "   %s\n", oneLine(e.Body, 200))
		}
	}
	sb.WriteString("/memory approve <n|name|all> saves · /memory reject <n|name|all> drops")
	return sb.String()
}
