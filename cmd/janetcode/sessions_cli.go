package main

import (
	"fmt"
	"io"
	"os"

	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/textfmt"
	"github.com/xmonader/janetcode/internal/whatif"
)

// showSession prints a saved session's conversation as Markdown. It reads
// the file and takes no lease, so it works on a session another janetcode
// has open: the read-only view a refused --resume points at.
func showSession(w io.Writer, arg string) error {
	_, dir, id, err := whatif.ResolveSession(arg)
	if err != nil {
		return err
	}
	s, msgs, err := session.Open(dir, id)
	if err != nil {
		return err
	}
	fmt.Fprint(w, session.Markdown(s.Meta, msgs))
	if in, ok := session.Holder(s.Path()); ok {
		fmt.Fprintf(os.Stderr, "(open in %s, pid %d: this is what it had written when read)\n", in.Holder, in.PID)
	}
	return nil
}

// searchSessionsMode is --search: every saved session whose conversation
// holds all the words, newest first, this directory's marked as such.
func searchSessionsMode(w io.Writer, query string) error {
	dir, err := session.Dir()
	if err != nil {
		return err
	}
	cwd, _ := os.Getwd()
	hits, err := session.Search(dir, "", query, 30)
	if err != nil {
		return err
	}
	if len(hits) == 0 {
		fmt.Fprintf(w, "no session mentions %q\n", query)
		return nil
	}
	for _, h := range hits {
		where := ""
		if h.Info.Meta.Cwd != cwd {
			where = "  in " + h.Info.Meta.Cwd
		}
		turn := ""
		if h.Turn > 0 {
			turn = fmt.Sprintf(" turn %d", h.Turn)
		}
		fmt.Fprintf(w, "%s  %s  %s%s\n    %s%s: %s\n", h.Info.Meta.ID, h.Info.Meta.Created.Local().Format("2006-01-02 15:04"), textfmt.Clip(h.Info.Label(), 60), where, h.Role, turn, h.Snippet)
	}
	fmt.Fprintln(w, "\nopen one with: janetcode --resume <id>   read it with: janetcode --show <id>")
	return nil
}
