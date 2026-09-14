package proof

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/cmdresult"
)

// ExecTimeout bounds one /proof (or `janetcode --proof`) invocation.
const ExecTimeout = 15 * time.Second

// AttachFunc builds and attaches a proof note for commit sha in the
// repository at root, returning its Summary. A live session supplies one
// (see agent.Agent.AttachProof); the CLI, which has no session's changes
// to build a note from, passes nil and only ever shows.
type AttachFunc func(ctx context.Context, root, sha string) (string, error)

// Exec runs one parsed /proof invocation for every front-end — the
// terminal, the web UI and `janetcode --proof`: push or fetch proof notes
// (see Push, Fetch, ConfigureFetch), or show the note on a commit,
// attaching one first with attach when there is none yet and attach is
// not nil. dir may be anywhere inside the repository. Errors are worded
// "proof ...: ...".
func Exec(ctx context.Context, dir string, a Args, attach AttachFunc) (cmdresult.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, ExecTimeout)
	defer cancel()
	root, err := Root(ctx, dir)
	if err != nil {
		return cmdresult.Result{}, fmt.Errorf("proof: %w", err)
	}
	switch a.Action {
	case ActionPush:
		warning, err := Push(ctx, root, a.Remote)
		if err != nil {
			return cmdresult.Result{}, fmt.Errorf("proof push: %w", err)
		}
		res := cmdresult.Result{Notes: []string{"pushed proof notes to " + a.Remote}}
		if warning != "" {
			res.Notes = append(res.Notes, warning)
		}
		return res, nil
	case ActionFetch:
		if err := Fetch(ctx, root, a.Remote); err != nil {
			return cmdresult.Result{}, fmt.Errorf("proof fetch: %w", err)
		}
		if err := ConfigureFetch(ctx, root, a.Remote); err != nil {
			return cmdresult.Result{}, fmt.Errorf("proof fetch: %w", err)
		}
		return cmdresult.Result{Notes: []string{"fetched proof notes from " + a.Remote + " · future fetches from it will bring them too"}}, nil
	}
	text, sha, err := Show(ctx, root, a.Commit, attach)
	if err != nil {
		return cmdresult.Result{}, fmt.Errorf("proof: %w", err)
	}
	if text == "" {
		return cmdresult.Result{Notes: []string{"no proof for " + Short(sha)}}, nil
	}
	return cmdresult.Result{Sheet: text, SheetTitle: "proof"}, nil
}

// Show returns the Summary of the proof note on commit (default HEAD) in
// the repository at root, and the full sha commit resolved to. When the
// commit carries no note, it returns attach's summary instead, or "" when
// attach is nil.
func Show(ctx context.Context, root, commit string, attach AttachFunc) (text, sha string, err error) {
	commit = strings.TrimSpace(commit)
	if commit == "" {
		commit = "HEAD"
	}
	sha, err = Resolve(ctx, root, commit)
	if err != nil {
		return "", "", fmt.Errorf("%s does not resolve to a commit: %w", commit, err)
	}
	r, ok, err := Read(ctx, root, sha)
	if err != nil {
		return "", sha, err
	}
	if ok {
		return Summary(r), sha, nil
	}
	if attach == nil {
		return "", sha, nil
	}
	text, err = attach(ctx, root, sha)
	return text, sha, err
}
