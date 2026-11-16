package agent

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/hook"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

// treeState fingerprints every file, directory and symlink under root
// (the .git directory included): path, mode, and content hash. Two equal
// states mean nothing on disk was created, removed, renamed or rewritten.
func treeState(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			dst, _ := os.Readlink(p)
			out[rel] = "link " + dst
		case d.IsDir():
			out[rel] = "dir " + info.Mode().String()
		default:
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			out[rel] = fmt.Sprintf("%s %x", info.Mode(), sha256.Sum256(data))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func diffStates(before, after map[string]string) string {
	var lines []string
	for p, v := range before {
		switch w, ok := after[p]; {
		case !ok:
			lines = append(lines, "removed "+p)
		case w != v:
			lines = append(lines, "changed "+p)
		}
	}
	for p := range after {
		if _, ok := before[p]; !ok {
			lines = append(lines, "created "+p)
		}
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// TestDryRunIsMutationFreeOnARealRepo is the guarantee itself, checked
// against a real git repository: a dry-run turn that tries every way a
// model changes things — file writes and edits, a delete, shell commands
// that create, remove, move and commit, a branch, a background job,
// every hook event, the verify gate — and then is discarded and ended
// leaves the whole tree, .git included, byte for byte as it was. The
// session directory gains the transcript and nothing else.
func TestDryRunIsMutationFreeOnARealRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	// A stray write to the user's home or config would be a leak too.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))

	repo := t.TempDir()
	git(t, repo, "init", "-q")
	for name, body := range map[string]string{"main.go": "package main\n", "README.md": "hello\n", "old.txt": "bye\n"} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git(t, repo, "add", "main.go", "README.md", "old.txt")
	git(t, repo, "commit", "-q", "-m", "init")
	headBefore := strings.TrimSpace(git(t, repo, "rev-parse", "HEAD"))
	repoBefore := treeState(t, repo)
	homeBefore := treeState(t, home)

	f := &fake{responses: []*llm.Response{
		toolCall("c1", "write_file", `{"path":"main.go","content":"package main\n\nfunc main() {}\n"}`),
		toolCall("c2", "edit_file", `{"path":"README.md","old_string":"hello","new_string":"hello, world"}`),
		toolCall("c3", "write_file", `{"path":"brand/new.go","content":"package brand\n"}`),
		toolCall("c4", "bash", `{"command":"rm old.txt && touch created && mkdir newdir && mv README.md moved.md"}`),
		toolCall("c5", "bash", `{"command":"git checkout -q -b feature && git add -A && git commit -q -m dry"}`),
		toolCall("c6", "bash", `{"command":"git reset --hard HEAD~0; echo x >> main.go"}`),
		toolCall("c7", "bash", `{"command":"sleep 1; touch late","background":true}`),
		toolCall("c8", "bash", `{"command":"git status --short"}`),
		text("planned"),
	}}
	a, _ := newAgent(t, f, nil)
	ws := tool.NewLocal(repo)
	a.Tools = tool.NewRegistry(tool.ReadFile{WS: ws}, tool.WriteFile{WS: ws}, tool.EditFile{WS: ws}, tool.Bash{WS: ws})
	a.AutoApprove = true
	hs := hook.Hooks{}
	for _, ev := range []string{hook.UserPrompt, hook.PreTool, hook.PostTool, hook.Stop, hook.SessionStart} {
		hs[ev] = []hook.Hook{{Command: "touch " + filepath.Join(repo, "hook-"+ev)}}
	}
	a.SetHooks(&hook.Runner{Cwd: repo, Config: hook.Config{Hooks: hs}})

	sessDir := t.TempDir()
	sess, err := session.New(sessDir, repo, "p", "m")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.OpenSession(sess, nil); err != nil {
		t.Fatal(err)
	}
	sessBefore := treeState(t, sessDir)

	a.SetDryRun(true)
	if err := a.Run(context.Background(), "do everything", nil); err != nil {
		t.Fatal(err)
	}
	// The plan is real: the previewed files and the unrun commands are in it.
	plan := a.DryRunPlan()
	if len(plan.Files) != 3 {
		t.Fatalf("plan files = %d, want 3: %+v", len(plan.Files), plan.Files)
	}
	cmds := 0
	for _, x := range plan.Actions {
		if x.Kind == "command" {
			cmds++
		}
	}
	if cmds != 4 {
		t.Fatalf("plan commands = %d, want 4: %+v", cmds, plan.Actions)
	}

	check := func(when string) {
		t.Helper()
		if d := diffStates(repoBefore, treeState(t, repo)); d != "" {
			t.Fatalf("%s: the repository changed:\n%s", when, d)
		}
		if h := strings.TrimSpace(git(t, repo, "rev-parse", "HEAD")); h != headBefore {
			t.Fatalf("%s: HEAD moved %s -> %s", when, headBefore, h)
		}
		if b := strings.TrimSpace(git(t, repo, "branch", "--show-current")); b == "feature" {
			t.Fatalf("%s: a branch was created and checked out", when)
		}
		if d := diffStates(homeBefore, treeState(t, home)); d != "" {
			t.Fatalf("%s: something was written under HOME:\n%s", when, d)
		}
		if len(a.Checkpoints()) != 0 {
			t.Fatalf("%s: a checkpoint was taken", when)
		}
	}
	check("after the dry-run turn")
	// The background job had time to run if it was going to.
	if _, err := os.Stat(filepath.Join(repo, "late")); err == nil {
		t.Fatal("the background job ran")
	}

	// Session dir: only the transcript and its audit log.
	for p, v := range diffSet(sessBefore, treeState(t, sessDir)) {
		if !strings.HasSuffix(p, ".jsonl") && !strings.HasSuffix(p, ".audit") {
			t.Errorf("session dir: %s %s (only the transcript and its audit log may change)", v, p)
		}
	}

	if _, err := a.DryRunCommand(context.Background(), "discard"); err != nil {
		t.Fatal(err)
	}
	check("after discard")
	if _, err := a.DryRunCommand(context.Background(), "off"); err != nil {
		t.Fatal(err)
	}
	check("after off")
}

// diffSet is diffStates as a path -> verb map.
func diffSet(before, after map[string]string) map[string]string {
	out := map[string]string{}
	for _, l := range strings.Split(diffStates(before, after), "\n") {
		if verb, p, ok := strings.Cut(l, " "); ok {
			out[p] = verb
		}
	}
	return out
}
