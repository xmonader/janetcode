package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/xmonader/janetcode/internal/proof"
)

// runProofFlag implements `janetcode --proof [commit]`: it prints the
// proof note (see internal/proof) already attached to commit, default
// HEAD. Unlike /proof in a live session it never attaches one: there is
// no session's tracked changes to build one from here, only whatever an
// earlier session already left on the commit.
func runProofFlag(w io.Writer, cwd, commit string) error {
	if commit == "" {
		commit = "HEAD"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	root, err := proof.Root(ctx, cwd)
	if err != nil {
		return fmt.Errorf("--proof: %w", err)
	}
	sha, err := proof.Resolve(ctx, root, commit)
	if err != nil {
		return fmt.Errorf("--proof: %s does not resolve to a commit: %w", commit, err)
	}
	r, ok, err := proof.Read(ctx, root, sha)
	if err != nil {
		return fmt.Errorf("--proof: %w", err)
	}
	if !ok {
		fmt.Fprintf(w, "no proof for %s\n", proof.Short(sha))
		return nil
	}
	fmt.Fprintln(w, proof.Summary(r))
	return nil
}
