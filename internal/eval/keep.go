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
// uncommitted changes) into Dir and refuses to overwrite an existing task.
func Keep(opts KeepOptions) (path string, task Task, err error) {
	sess := opts.Session
	if sess == nil {
		return "", Task{}, fmt.Errorf("no session to keep")
	}
	start := sess.Meta.Start
	if start == nil || start.Head == "" {
		return "", Task{}, fmt.Errorf("this session has no recorded start state, so there is nothing to replay it against: it began before this feature existed, before this repository had any commit, or outside a git repository")
	}
	prompts := userPrompts(sess.Events())
	if len(prompts) == 0 {
		return "", Task{}, fmt.Errorf("no user prompts found in this session")
	}
	check := strings.TrimSpace(opts.Check)
	if check == "" {
		cfg, _, verr := verify.Load(opts.Root)
		if verr != nil {
			return "", Task{}, fmt.Errorf("load %s: %w", verify.ProjectFile, verr)
		}
		check = strings.Join(cfg.Commands, " && ")
	}
	if check == "" {
		return "", Task{}, fmt.Errorf("no check command: pass --check <command>, or add commands to %s", verify.ProjectFile)
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
		return "", Task{}, err
	}
	taskPath := filepath.Join(dir, name+".json")
	if _, statErr := os.Stat(taskPath); statErr == nil {
		return "", Task{}, fmt.Errorf("%s already exists; pass a different name", taskPath)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return "", Task{}, statErr
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
		Base:    start.Head,
		dir:     dir,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	patch, derr := gitDiff(ctx, opts.Root, start.Head, start.Snap)
	if derr != nil {
		return "", Task{}, fmt.Errorf("diff session start: %w", derr)
	}
	if len(bytes.TrimSpace(patch)) > 0 {
		patchName := name + ".patch"
		if err := fsutil.WriteFileAtomic(filepath.Join(dir, patchName), patch, 0o644); err != nil {
			return "", Task{}, fmt.Errorf("write patch: %w", err)
		}
		t.Patch = patchName
	}

	data, merr := json.MarshalIndent(t, "", "  ")
	if merr != nil {
		return "", Task{}, merr
	}
	if err := fsutil.WriteFileAtomic(taskPath, append(data, '\n'), 0o644); err != nil {
		return "", Task{}, err
	}
	return taskPath, t, nil
}

// ParseKeepArgs reads the argument to /eval keep: arg is the text after
// "keep". A bare word (or several, joined) names the task; --check takes
// the rest of the line as the check command, so it must come last.
func ParseKeepArgs(arg string) (name, check string, err error) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return "", "", nil
	}
	if i := strings.Index(arg, "--check"); i >= 0 {
		check = strings.TrimSpace(arg[i+len("--check"):])
		if check == "" {
			return "", "", fmt.Errorf("usage: /eval keep [name] [--check <command>]")
		}
		return strings.TrimSpace(arg[:i]), check, nil
	}
	return arg, "", nil
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
