package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/xmonader/janetcode/internal/headless"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/whatif"
	"github.com/xmonader/janetcode/internal/whybroke"
)

// runWhyBrokeFlag implements `janetcode --why-broke <test> [session]`
// through whybroke.Run, the way --proof verify runs proof.Exec: a bool
// flag whose arguments are positional, offline, no provider needed. The
// last argument is taken as the session when it names one (an id, or a
// path to its .jsonl); otherwise the newest session recorded for cwd is
// bisected. A test with a space in it is a command, so it is one quoted
// argument.
func runWhyBrokeFlag(ctx context.Context, w io.Writer, cwd string, args []string) error {
	if len(args) == 0 {
		return headless.BadInput("--why-broke needs the failing test: janetcode --why-broke <test or 'command'> [session]")
	}
	path := ""
	if len(args) > 1 {
		if p, _, _, err := whatif.ResolveSession(args[len(args)-1]); err == nil {
			path, args = p, args[:len(args)-1]
		}
	}
	if path == "" {
		dir, err := session.Dir()
		if err != nil {
			return err
		}
		id, err := session.Latest(dir, cwd)
		if err != nil {
			return err
		}
		if id == "" {
			return headless.BadInput("--why-broke: no session recorded for %s; name one (janetcode --sessions lists them)", cwd)
		}
		if path, _, _, err = whatif.ResolveSession(id); err != nil {
			return err
		}
	}
	rep, err := whybroke.Run(ctx, whybroke.Options{
		Test:        strings.Join(args, " "),
		SessionPath: path,
		Log:         func(format string, a ...any) { fmt.Fprintf(os.Stderr, "[why-broke] "+format+"\n", a...) },
	})
	if errors.Is(err, whybroke.ErrBadArgument) {
		return headless.BadInput("--why-broke: %v", err)
	}
	if err != nil {
		return fmt.Errorf("--why-broke: %w", err)
	}
	fmt.Fprint(w, rep.Text())
	return nil
}
