package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/prompt"
)

// kernelTestRepo fills dir with a committed Go module of n files, too
// many to be sent whole, one of which defines RetryBudget.
func kernelTestRepo(t *testing.T, dir string, n int) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	write := func(rel, body string) {
		p := filepath.Join(dir, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module x\n\ngo 1.22\n")
	for i := 0; i < n; i++ {
		write(fmt.Sprintf("p/f%02d.go", i), fmt.Sprintf("package p\n\nfunc F%d() int { return %d }\n", i, i))
	}
	write("p/retry.go", "package p\n\n// RetryBudget caps the retries a call may make.\nfunc RetryBudget(n int) int {\n\treturn n * 2\n}\n")
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "init"}} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

func TestPrefixKernelAndPreresolveWiring(t *testing.T) {
	d, sess := buildTestDeps(t)
	kernelTestRepo(t, d.cwd, 45)
	d.cfg.PrefixKernel = true
	d.cfg.Preresolve = true
	b, err := buildAgent(d, sess, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.close)
	ag := b.agent

	if strings.Contains(ag.System, prompt.EnvironmentHeading) || strings.Contains(ag.System, d.cwd) {
		t.Fatalf("the kernel prompt still carries the environment:\n%s", ag.System)
	}
	if !strings.Contains(ag.System, "[repository map of the committed tree") {
		t.Fatalf("the kernel prompt has no map of the committed tree:\n%s", ag.System)
	}
	if got, _ := filepath.Glob(filepath.Join(filepath.Dir(d.sessDir), "kernels", "*.map")); len(got) != 1 {
		t.Fatalf("the kernel map was not stored beside the sessions: %v", got)
	}

	blocks, note := ag.Prepare(context.Background(), []llm.Block{{Type: llm.BlockText, Text: "Make RetryBudget triple n instead."}})
	if !strings.Contains(note, "resolved from the request: 1 definition(s)") {
		t.Fatalf("note = %q", note)
	}
	var resolved bool
	for _, bl := range blocks {
		if strings.Contains(bl.Text, `<definition symbol="RetryBudget" path="p/retry.go"`) {
			resolved = true
		}
	}
	if !resolved {
		t.Fatalf("the definition was not attached: %+v", blocks)
	}
	last := blocks[len(blocks)-1]
	if !last.Synthetic || !strings.HasPrefix(last.Text, "<environment>") || !strings.Contains(last.Text, d.cwd) {
		t.Fatalf("the environment is not the last block of the first message: %q", last.Text)
	}
}

// Off, neither changes anything: the usual prompt, and no resolved block
// or environment note in the first message.
func TestPrefixKernelAndPreresolveOffByDefault(t *testing.T) {
	d, sess := buildTestDeps(t)
	kernelTestRepo(t, d.cwd, 45)
	b, err := buildAgent(d, sess, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.close)
	if !strings.Contains(b.agent.System, prompt.EnvironmentHeading) {
		t.Fatal("the usual prompt lost its environment with the kernel off")
	}
	blocks, _ := b.agent.Prepare(context.Background(), []llm.Block{{Type: llm.BlockText, Text: "Make RetryBudget triple n instead."}})
	for _, bl := range blocks {
		if strings.HasPrefix(bl.Text, "<resolved>") || strings.HasPrefix(bl.Text, "<environment>") {
			t.Fatalf("an off feature attached %q", bl.Text[:20])
		}
	}
}

// A repository sent whole needs nothing resolved: the hook stands aside.
func TestPreresolveStandsAsideForAnInlinedRepo(t *testing.T) {
	d, sess := buildTestDeps(t)
	kernelTestRepo(t, d.cwd, 3)
	d.cfg.Preresolve = true
	b, err := buildAgent(d, sess, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.close)
	blocks, note := b.agent.Prepare(context.Background(), []llm.Block{{Type: llm.BlockText, Text: "Make RetryBudget triple n instead."}})
	if strings.Contains(note, "resolved from the request") {
		t.Fatalf("resolved against a repository already attached whole: %q", note)
	}
	if !attachedWhole(blocks) {
		t.Fatal("the small repository was not attached whole; the test proves nothing")
	}
}
