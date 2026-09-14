package main

import (
	"context"
	"fmt"
	"io"

	"github.com/xmonader/janetcode/internal/proof"
)

// runProofFlag implements `janetcode --proof [commit | push [remote] |
// fetch [remote]]` through proof.Exec, the same code /proof runs in a live
// session. Unlike /proof it never attaches a note (it passes no
// AttachFunc): there is no session's tracked changes to build one from
// here, only whatever an earlier session already left on the commit.
func runProofFlag(w io.Writer, cwd, args string) error {
	res, err := proof.Exec(context.Background(), cwd, proof.ParseArgs(args), nil)
	if err != nil {
		return fmt.Errorf("--%w", err)
	}
	if res.Sheet != "" {
		fmt.Fprintln(w, res.Sheet)
	}
	for _, n := range res.Notes {
		fmt.Fprintln(w, n)
	}
	return nil
}
