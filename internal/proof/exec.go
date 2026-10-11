package proof

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/cmdresult"
	"github.com/xmonader/janetcode/internal/gitx"
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
	timeout := ExecTimeout
	if a.Action == ActionVerify && !a.Static {
		timeout = VerifyTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if a.Action == ActionKey {
		return keyResult()
	}
	root, err := Root(ctx, dir)
	if err != nil && gitx.ExitCode(err) <= 0 {
		return cmdresult.Result{}, fmt.Errorf("proof: %w", err) // git itself did not run
	}
	if err != nil {
		return cmdresult.Result{}, &noCommit{msg: "not inside a git repository; proof notes live on commits", cause: err}
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
	case ActionVerify:
		return verifyResult(ctx, root, a)
	}
	text, sha, err := Show(ctx, root, a.Commit, attach)
	if errors.Is(err, ErrNoCommit) {
		return cmdresult.Result{}, err
	}
	if err != nil {
		return cmdresult.Result{}, fmt.Errorf("proof: %w", err)
	}
	if text == "" {
		return cmdresult.Result{Notes: []string{"no proof for " + Short(sha)}}, nil
	}
	return cmdresult.Result{Sheet: text, SheetTitle: "proof"}, nil
}

// VerifyTimeout bounds `janetcode --proof verify`, which re-runs every
// recorded command: CommandTimeout each, and this for all of them.
const VerifyTimeout = time.Hour

// verifyResult runs VerifyEvidence for /proof verify and --proof verify.
// Evidence that does not hold — no evidence, a bad or untrusted
// signature, another tree, a re-run that exited differently — is
// reported in the result's Error after the sheet, so the CLI can exit
// non-zero while still printing what it found.
func verifyResult(ctx context.Context, root string, a Args) (cmdresult.Result, error) {
	commit := strings.TrimSpace(a.Commit)
	if commit == "" {
		commit = "HEAD"
	}
	rep, err := VerifyEvidence(ctx, root, commit, VerifyOptions{Run: !a.Static})
	if errors.Is(err, ErrNoCommit) {
		return cmdresult.Result{}, err
	}
	if err != nil {
		return cmdresult.Result{}, fmt.Errorf("proof verify: %w", err)
	}
	res := cmdresult.Result{Sheet: VerifySummary(rep), SheetTitle: "proof verify"}
	if a.Static {
		res.Notes = append(res.Notes, "static check only; `janetcode --proof verify "+Short(rep.Commit)+"` from a shell re-runs the recorded commands")
	}
	if !rep.OK() {
		res.Error = "the evidence on " + Short(rep.Commit) + " does not hold"
	}
	return res, nil
}

// keyResult is /proof key: the local evidence key's trusted-file line,
// creating the key if this machine has none yet.
func keyResult() (cmdresult.Result, error) {
	dir, err := DefaultKeyDir()
	if err != nil {
		return cmdresult.Result{}, fmt.Errorf("proof key: %w", err)
	}
	pub, err := NewKeySigner(dir).PublicKey()
	if err != nil {
		return cmdresult.Result{}, fmt.Errorf("proof key: %w", err)
	}
	return cmdresult.Result{
		Sheet: TrustLine(pub), SheetTitle: "evidence key", Plain: true,
		Notes: []string{"another machine verifies this one's evidence once that line is in its " + filepath.Join(dir, trustedFile)},
	}, nil
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
		return "", "", notACommit(ctx, root, commit, err)
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

// ErrNoCommit is what Show returns when the commit asked for is not there:
// not a repository, a repository with no commits yet, or a name that
// names nothing. The message says which, in words; git's own ("fatal: not
// a git repository (or any parent up to mount point /)", "exit status
// 1") is kept only as the wrapped cause.
var ErrNoCommit = errors.New("no such commit")

func notACommit(ctx context.Context, root, commit string, cause error) error {
	var msg string
	switch {
	case gitx.CommitExists(ctx, root, "HEAD") == nil:
		msg = "no commit named " + commit + " in this repository"
	default:
		msg = "this repository has no commits yet; proof notes live on commits"
	}
	return &noCommit{msg: msg, cause: cause}
}

type noCommit struct {
	msg   string
	cause error
}

func (e *noCommit) Error() string   { return e.msg }
func (e *noCommit) Unwrap() []error { return []error{ErrNoCommit, e.cause} }
