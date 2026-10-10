package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/gitx"
	"github.com/xmonader/janetcode/internal/headless"
	"github.com/xmonader/janetcode/internal/janetref"
	"github.com/xmonader/janetcode/internal/proof"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// sessionRefUsage is --session-ref's argument list.
const sessionRefUsage = "--session-ref save [id] | list | show <id> | open <id> | push [remote] | fetch [remote] [id] | probes"

// sessionRefCommits is how far back from HEAD save looks for commits
// whose proof note names the session.
const sessionRefCommits = 50

// runSessionRefFlag implements `janetcode --session-ref ...` (see
// internal/janetref): sessions kept in the repository as refs, so they
// travel with a push and a reviewer can open the session behind a change.
func runSessionRefFlag(w io.Writer, cwd string, args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	root, err := proof.Root(ctx, cwd)
	if err != nil {
		return fmt.Errorf("--session-ref needs a git repository: %w", err)
	}
	if len(args) == 0 {
		args = []string{"list"}
	}
	arg := func(i int) string {
		if i < len(args) {
			return args[i]
		}
		return ""
	}
	switch args[0] {
	case "save":
		return sessionRefSave(ctx, w, root, cwd, arg(1))
	case "list":
		return sessionRefList(ctx, w, root)
	case "show":
		return sessionRefShow(ctx, w, root, arg(1))
	case "open":
		dir, err := session.Dir()
		if err != nil {
			return err
		}
		path, err := janetref.OpenSession(ctx, root, arg(1), dir)
		if err != nil {
			return err
		}
		fmt.Fprintf(w, "opened %s as %s\nread it with: janetcode --show %s   re-run its evidence with: janetcode --proof verify <commit>\n", arg(1), path, arg(1))
		return nil
	case "push":
		refs, err := janetref.Push(ctx, root, arg(1), janetref.SessionsPrefix+"*", janetref.ProbesRef)
		if err != nil {
			return err
		}
		if len(refs) == 0 {
			fmt.Fprintln(w, "nothing to push: no saved session and no probe memory here")
			return nil
		}
		fmt.Fprintf(w, "pushed %s\n", textfmt.Plural(len(refs), "ref"))
		return nil
	case "fetch":
		if err := janetref.FetchSessions(ctx, root, arg(1), arg(2)); err != nil {
			return err
		}
		msg := "fetched saved sessions"
		// A remote that never had a probe memory pushed has none: not a
		// failure of the fetch that was asked for.
		if n, err := janetref.FetchProbes(ctx, root, arg(1)); err == nil {
			msg += fmt.Sprintf("; probe memory: %d new", n)
		}
		fmt.Fprintln(w, msg)
		return nil
	case "probes":
		probes, _, err := janetref.LoadProbes(ctx, root)
		if err != nil {
			return err
		}
		if len(probes) == 0 {
			fmt.Fprintln(w, "the probe memory is empty (config probe_memory fills it)")
			return nil
		}
		for _, p := range probes {
			fmt.Fprintf(w, "%s  %s  %s\n    %s\n", p.Added.Local().Format("2006-01-02"), orDashStr(p.Session), textfmt.Clip(p.Why, 80), textfmt.FirstLine(p.Cmd, 120))
		}
		return nil
	}
	return headless.BadInput("unknown --session-ref command %q: %s", args[0], sessionRefUsage)
}

func orDashStr(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// sessionRefSave stores one session: its transcript, the proof records
// and signed evidence of recent commits that name it, and the probes it
// left in the memory.
func sessionRefSave(ctx context.Context, w io.Writer, root, cwd, id string) error {
	dir, err := session.Dir()
	if err != nil {
		return err
	}
	if id == "" {
		if id, err = session.Latest(dir, cwd); err != nil {
			return err
		}
		if id == "" {
			return headless.BadInput("no session recorded for this directory; name one: %s", sessionRefUsage)
		}
	}
	if !janetref.ValidID(id) {
		return headless.BadInput("%q is not a session id", id)
	}
	raw, err := os.ReadFile(filepath.Join(dir, id+".jsonl"))
	if err != nil {
		return err
	}
	parts := janetref.SessionParts{Transcript: raw, Evidence: sessionRefEvidence(ctx, root, id)}
	if probes, _, err := janetref.LoadProbes(ctx, root); err == nil {
		for _, p := range probes {
			if p.Session == id {
				parts.Probes = append(parts.Probes, p)
			}
		}
	}
	info, err := janetref.SaveSession(ctx, root, id, parts, 0)
	if err != nil {
		return err
	}
	clip := "whole"
	if info.Clipped > 0 {
		clip = fmt.Sprintf("long strings clipped to %d bytes to stay under %d KB", info.Clipped, janetref.MaxSessionBytes>>10)
	}
	fmt.Fprintf(w, "saved %s under %s%s (%s)\npush it with: janetcode --session-ref push\n", id, janetref.SessionsPrefix, id, clip)
	return nil
}

// sessionRefEvidence is the proof records, and signed evidence where
// there is any, of the commits near HEAD whose proof note names session
// id, as JSON; nil when there are none.
func sessionRefEvidence(ctx context.Context, root, id string) []byte {
	out, err := gitx.Run(ctx, root, nil, "rev-list", fmt.Sprintf("--max-count=%d", sessionRefCommits), "HEAD")
	if err != nil {
		return nil
	}
	type entry struct {
		Commit   string          `json:"commit"`
		Proof    proof.Record    `json:"proof"`
		Evidence *proof.Envelope `json:"evidence,omitempty"`
	}
	var entries []entry
	for _, sha := range strings.Fields(out) {
		r, ok, err := proof.Read(ctx, root, sha)
		if err != nil || !ok || !sessionRefNames(r, id) {
			continue
		}
		e := entry{Commit: sha, Proof: r}
		if env, ok, err := proof.ReadEvidence(ctx, root, sha); err == nil && ok {
			e.Evidence = &env
		}
		entries = append(entries, e)
	}
	if len(entries) == 0 {
		return nil
	}
	data, _ := json.MarshalIndent(entries, "", "  ")
	return data
}

// sessionRefNames reports whether a proof record has work of session id.
func sessionRefNames(r proof.Record, id string) bool {
	if r.Session == id {
		return true
	}
	for _, f := range r.Files {
		for _, c := range f.Changes {
			if c.Session == id {
				return true
			}
		}
	}
	return false
}

func sessionRefList(ctx context.Context, w io.Writer, root string) error {
	saved, err := janetref.ListSessions(ctx, root)
	if err != nil {
		return err
	}
	if len(saved) == 0 {
		fmt.Fprintln(w, "no saved sessions (janetcode --session-ref save; fetch others' with --session-ref fetch)")
		return nil
	}
	for _, s := range saved {
		fmt.Fprintf(w, "%s  %s  %4d KB  %s\n", s.ID, s.Saved.Local().Format("2006-01-02 15:04"), (s.Bytes+1023)>>10, textfmt.Clip(s.Title, 60))
	}
	fmt.Fprintln(w, "\nopen one with: janetcode --session-ref open <id>")
	return nil
}

func sessionRefShow(ctx context.Context, w io.Writer, root, id string) error {
	if !janetref.ValidID(id) {
		return headless.BadInput("%q is not a session id: %s", id, sessionRefUsage)
	}
	tip, err := janetref.Tip(ctx, root, janetref.SessionsPrefix+id)
	if err != nil {
		return err
	}
	if tip == "" {
		return fmt.Errorf("no saved session %s", id)
	}
	if data, ok, _ := janetref.ReadAt(ctx, root, tip, janetref.SessionInfo); ok {
		fmt.Fprintf(w, "%s\n", strings.TrimSpace(string(data)))
	}
	files, err := janetref.Files(ctx, root, tip)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(w, "  %-18s %8d bytes\n", name, files[name])
	}
	return nil
}
