package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/xmonader/janetcode/internal/proof"
)

// runProofFlag implements `janetcode --proof [commit | push [remote] |
// fetch [remote]]`. With a commit (default HEAD) it prints the proof note
// (see internal/proof) already attached to it. Unlike /proof in a live
// session it never attaches one: there is no session's tracked changes to
// build one from here, only whatever an earlier session already left on
// the commit. push and fetch move proof notes to and from a remote (see
// internal/proof.Push, Fetch, ConfigureFetch): notes are local only until
// pushed.
func runProofFlag(w io.Writer, cwd, args string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	root, err := proof.Root(ctx, cwd)
	if err != nil {
		return fmt.Errorf("--proof: %w", err)
	}
	a := proof.ParseArgs(args)
	switch a.Action {
	case proof.ActionPush:
		warning, err := proof.Push(ctx, root, a.Remote)
		if err != nil {
			return fmt.Errorf("--proof push: %w", err)
		}
		fmt.Fprintf(w, "pushed proof notes to %s\n", a.Remote)
		if warning != "" {
			fmt.Fprintln(w, warning)
		}
		return nil
	case proof.ActionFetch:
		if err := proof.Fetch(ctx, root, a.Remote); err != nil {
			return fmt.Errorf("--proof fetch: %w", err)
		}
		if err := proof.ConfigureFetch(ctx, root, a.Remote); err != nil {
			return fmt.Errorf("--proof fetch: %w", err)
		}
		fmt.Fprintf(w, "fetched proof notes from %s (future fetches from it will bring them too)\n", a.Remote)
		return nil
	}
	commit := a.Commit
	if commit == "" {
		commit = "HEAD"
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
