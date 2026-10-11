package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/xmonader/janetcode/internal/headless"
	"github.com/xmonader/janetcode/internal/proof"
)

// runProofFlag implements `janetcode --proof [commit | push [remote] |
// fetch [remote] | verify [commit] | key]` through proof.Exec, the same
// code /proof runs in a live session. Unlike /proof it never attaches a note (it passes no
// AttachFunc): there is no session's tracked changes to build one from
// here, only whatever an earlier session already left on the commit.
func runProofFlag(w io.Writer, cwd, args string) error {
	res, err := proof.Exec(context.Background(), cwd, proof.ParseArgs(args), nil)
	if errors.Is(err, proof.ErrNoCommit) {
		// A commit that is not there is the caller's input, not a
		// failure of the command: the usage status, and no "proof: " prefix.
		return headless.BadInput("--proof: %v", err)
	}
	if err != nil {
		return fmt.Errorf("--%w", err)
	}
	if res.Sheet != "" {
		fmt.Fprintln(w, res.Sheet)
	}
	for _, n := range res.Notes {
		fmt.Fprintln(w, n)
	}
	if res.Error != "" {
		// --proof verify on evidence that does not hold: the report is
		// printed above, and the status says it failed.
		return errors.New("--proof: " + res.Error)
	}
	return nil
}
