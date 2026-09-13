package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/fsutil"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/memory"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/verify"
)

// KeepOptions is what /eval keep needs to turn a real session into a
// regression eval task.
type KeepOptions struct {
	// Root is the project directory: the repository the task will replay
	// against, and where a missing Check falls back to verify.json.
	Root string
	// Session is the conversation being kept.
	Session *session.Session
	// Name, when empty, defaults to a slug of the session's title (or id).
	Name string
	// Check, when empty, defaults to the project's verify.json commands
	// joined with " && "; an error if there are none either.
	Check string
	// Dir is where the task and its patch are written; the default is
	// Root/.janetcode/evals.
	Dir string
	// Base names a starting commit directly, resolved to a full sha and
	// recorded as Task.Base with no patch. It is the way to keep a
	// session that has no recorded Start (one begun before this feature
	// existed, or where recording failed), and it overrides a recorded
	// Start when given even if one exists.
	Base string
	// SelfContained makes the kept task independent of Base ever existing
	// in the clone that runs it: Task.Base stays empty and the whole
	// starting tree — the session's own snapshot, or Base's tree when
	// Base is set — travels as a patch against the empty tree instead.
	SelfContained bool
}

// Keep turns a session into a Task: its user prompts become Task.Prompts,
// and the tree it began from becomes Task.Repo/Base/Patch — the commit
// HEAD pointed to when the session's first turn ran, plus a patch of
// whatever was uncommitted at that moment (see session.StartState,
// recorded by internal/agent's recordStart). Running the task later
// therefore replays the session against the state it actually started
// from, not an empty directory: see docs/manual/evals.md.
//
// It writes <name>.json (and <name>.patch, when the session started with
// uncommitted changes, the repository was unborn, or SelfContained was
// asked for) into Dir and refuses to overwrite an existing task.
//
// A session begun in a repository with no commits yet has an empty
// Start.Head; Keep still keeps it, leaving Task.Base empty and writing
// the whole starting tree as a patch against the empty tree instead (see
// prepareWorkDir's no-Base path). A session with no recorded Start at
// all needs opts.Base.
//
// Warnings reports non-fatal problems the caller should show: today,
// that Task.Base is reachable from no remote-tracking ref, so a clone
// other than this one cannot run the task until it is pushed or the
// session is re-kept with SelfContained.
func Keep(opts KeepOptions) (path string, task Task, warnings []string, err error) {
	sess := opts.Session
	if sess == nil {
		return "", Task{}, nil, fmt.Errorf("no session to keep")
	}
	prompts := userPrompts(sess.Events())
	if len(prompts) == 0 {
		return "", Task{}, nil, fmt.Errorf("no user prompts found in this session")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Every path below reads the repository: the check command's default
	// source (verify.json lives in the tree), the base commit, and the
	// diff. Say so up front, in one place, rather than letting a
	// non-git Root surface as a misleading "no check command" (when
	// neither --check nor --base was given) or a raw `git` fatal error
	// (when --base was).
	if !isGitRepo(ctx, opts.Root) {
		return "", Task{}, nil, fmt.Errorf("not a git repository: /eval keep needs one (%s)", opts.Root)
	}

	check := strings.TrimSpace(opts.Check)
	if check == "" {
		cfg, _, verr := verify.Load(opts.Root)
		if verr != nil {
			return "", Task{}, nil, fmt.Errorf("load %s: %w", verify.ProjectFile, verr)
		}
		check = strings.Join(cfg.Commands, " && ")
	}
	if check == "" {
		return "", Task{}, nil, fmt.Errorf("no check command: pass --check <command>, or add commands to %s", verify.ProjectFile)
	}

	start := sess.Meta.Start
	base := strings.TrimSpace(opts.Base)
	var baseSHA string
	var patch []byte
	switch {
	case base != "":
		resolved, rerr := resolveRev(ctx, opts.Root, base)
		if rerr != nil {
			return "", Task{}, nil, rerr
		}
		if opts.SelfContained {
			patch, err = emptyTreeDiff(ctx, opts.Root, resolved)
		} else {
			baseSHA = resolved
		}
	case start != nil && start.Head != "":
		if opts.SelfContained {
			patch, err = emptyTreeDiff(ctx, opts.Root, start.Snap)
		} else {
			baseSHA = start.Head
			patch, err = gitDiff(ctx, opts.Root, start.Head, start.Snap)
		}
	case start != nil && start.Head == "":
		// The repository was unborn (no commits) when the session began:
		// there is no Base to record either way, so the whole starting
		// tree travels as a patch against the empty tree.
		patch, err = emptyTreeDiff(ctx, opts.Root, start.Snap)
	default:
		return "", Task{}, nil, fmt.Errorf("this session has no recorded start state, so there is nothing to replay it against: it began before this feature existed, before this repository had any commit, or outside a git repository; pass --base <rev> to choose a starting commit directly")
	}
	if err != nil {
		return "", Task{}, nil, fmt.Errorf("diff session start: %w", err)
	}

	if baseSHA != "" {
		ok, rerr := hasRemoteContaining(ctx, opts.Root, baseSHA)
		if rerr != nil {
			warnings = append(warnings, fmt.Sprintf("could not check whether %s is on a remote: %v", shortSHA(baseSHA), rerr))
		} else if !ok {
			warnings = append(warnings, fmt.Sprintf("base %s is only local: push it, or re-keep with --self-contained, so another clone can run this task", shortSHA(baseSHA)))
		}
	}

	name := strings.TrimSpace(opts.Name)
	if name == "" {
		name = sess.Label()
	}
	name = memory.Slug(name)

	dir := opts.Dir
	if dir == "" {
		dir = filepath.Join(opts.Root, ".janetcode", "evals")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", Task{}, nil, err
	}
	taskPath := filepath.Join(dir, name+".json")
	if _, statErr := os.Stat(taskPath); statErr == nil {
		return "", Task{}, nil, fmt.Errorf("%s already exists; pass a different name", taskPath)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return "", Task{}, nil, statErr
	}

	// Repo is written relative to the task file so the eval directory can
	// be committed and still resolve on another checkout of the same
	// repository, at whatever path it was cloned to.
	repo, relErr := filepath.Rel(dir, opts.Root)
	if relErr != nil {
		repo = opts.Root
	}

	t := Task{
		Name:    name,
		Prompts: prompts,
		Check:   check,
		Timeout: 900,
		Tags:    []string{"kept"},
		Repo:    repo,
		Base:    baseSHA,
		dir:     dir,
	}

	if len(bytes.TrimSpace(patch)) > 0 {
		patchName := name + ".patch"
		if err := fsutil.WriteFileAtomic(filepath.Join(dir, patchName), patch, 0o644); err != nil {
			return "", Task{}, nil, fmt.Errorf("write patch: %w", err)
		}
		t.Patch = patchName
	}

	data, merr := json.MarshalIndent(t, "", "  ")
	if merr != nil {
		return "", Task{}, nil, merr
	}
	if err := fsutil.WriteFileAtomic(taskPath, append(data, '\n'), 0o644); err != nil {
		return "", Task{}, nil, err
	}
	return taskPath, t, warnings, nil
}

// ParseKeepArgs reads the argument to /eval keep: arg is the text after
// "keep". A bare word (or several, joined) names the task. --base <rev>
// names a starting commit directly (see KeepOptions.Base) — the way to
// keep a session with no recorded start state, or to point at a
// different one. --self-contained asks for KeepOptions.SelfContained.
// --check takes the rest of the line as the check command, so it must
// come last; --base and --self-contained may appear anywhere before it.
func ParseKeepArgs(arg string) (name, check, base string, selfContained bool, err error) {
	const usage = "usage: /eval keep [name] [--base <rev>] [--self-contained] [--check <command>]"
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return "", "", "", false, nil
	}
	if i := strings.Index(arg, "--check"); i >= 0 {
		check = strings.TrimSpace(arg[i+len("--check"):])
		if check == "" {
			return "", "", "", false, fmt.Errorf(usage)
		}
		arg = strings.TrimSpace(arg[:i])
	}
	fields := strings.Fields(arg)
	var nameParts []string
	for i := 0; i < len(fields); i++ {
		switch fields[i] {
		case "--base":
			if i+1 >= len(fields) {
				return "", "", "", false, fmt.Errorf(usage)
			}
			i++
			base = fields[i]
		case "--self-contained":
			selfContained = true
		default:
			nameParts = append(nameParts, fields[i])
		}
	}
	return strings.Join(nameParts, " "), check, base, selfContained, nil
}

// userPrompts is the user prompts of a session, in order: what the user
// (or something acting on their behalf, like /goal or /undo) actually
// asked for, as opposed to tool results, later compaction summaries, or
// mid-turn steering notes. Each real user turn logs exactly one
// session.TypeUserMessage event at the moment it starts (see
// Agent.logUserMessage); a later steering note delivered mid-turn, or a
// compaction summary that replaces a span of earlier turns, logs another
// event carrying the same Turn number but a higher Seq, so keeping only
// the first event seen for each Turn recovers the original prompts and
// nothing else. Synthetic turns (session.Synthetic) and empty ones are
// dropped.
func userPrompts(events []session.Event) []string {
	first := map[int]session.Event{}
	var turns []int
	for _, e := range events {
		if e.Type != session.TypeUserMessage || e.Message == nil {
			continue
		}
		if _, ok := first[e.Turn]; ok {
			continue
		}
		first[e.Turn] = e
		turns = append(turns, e.Turn)
	}
	sort.Ints(turns)
	out := make([]string, 0, len(turns))
	for _, tn := range turns {
		text := strings.TrimSpace(messageText(first[tn].Message))
		if text == "" || session.Synthetic(text) {
			continue
		}
		out = append(out, text)
	}
	return out
}

// messageText is a message's user-written text: a text block the harness
// itself injected mid-turn (a path-scoped rule, a job update) is marked
// Synthetic and excluded, the same guard session.Synthetic gives a whole
// message.
func messageText(m *llm.Message) string {
	var sb strings.Builder
	for _, b := range m.Blocks {
		if b.Type == llm.BlockText && !b.Synthetic {
			sb.WriteString(b.Text)
		}
	}
	return sb.String()
}

// isGitRepo reports whether dir is inside a git working tree. Keep calls
// it once, up front, so a Root that is not a repository fails with one
// clear message instead of a misleading "no check command" or a raw
// `git` fatal error surfacing from whichever git command happened to run
// first (resolveRev for --base, gitDiff/emptyTreeDiff otherwise).
func isGitRepo(ctx context.Context, dir string) bool {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--is-inside-work-tree")
	cmd.Dir = dir
	return cmd.Run() == nil
}

func gitDiff(ctx context.Context, dir, head, snap string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", "diff", "--binary", head, snap)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("git diff: %s", msg)
	}
	return stdout.Bytes(), nil
}

// resolveRev turns --base's argument into a full commit sha, so Task.Base
// is stable even if the rev given (a branch, a tag, a short hash) later
// points somewhere else.
func resolveRev(ctx context.Context, dir, rev string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--verify", rev+"^{commit}")
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("--base %s: %s", rev, msg)
	}
	return strings.TrimSpace(stdout.String()), nil
}

// emptyTreeHash is the hash of the empty tree, computed rather than
// hardcoded so a sha256 repository gets its own value instead of git's
// well-known sha1 constant.
func emptyTreeHash(ctx context.Context, dir string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "hash-object", "-t", "tree", os.DevNull)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("empty tree hash: %s", msg)
	}
	return strings.TrimSpace(stdout.String()), nil
}

// emptyTreeDiff is the whole tree at treeish, expressed as a patch from
// nothing: how Keep represents a starting state with no base commit to
// point at, whether because the repository was unborn at session start
// or because SelfContained asked for one anyway.
func emptyTreeDiff(ctx context.Context, dir, treeish string) ([]byte, error) {
	empty, err := emptyTreeHash(ctx, dir)
	if err != nil {
		return nil, err
	}
	return gitDiff(ctx, dir, empty, treeish)
}

// hasRemoteContaining reports whether sha is reachable from any
// remote-tracking ref. Keep uses it to warn when a kept task's base
// commit exists only in the local clone that made it, so another clone
// running the task later would find no such commit to check out.
func hasRemoteContaining(ctx context.Context, dir, sha string) (bool, error) {
	cmd := exec.CommandContext(ctx, "git", "for-each-ref", "--contains", sha, "refs/remotes")
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return false, fmt.Errorf("check remote refs: %s", msg)
	}
	return strings.TrimSpace(stdout.String()) != "", nil
}

// shortSHA is a commit hash shortened for a human-readable message.
func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
