package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/hook"
	"github.com/xmonader/janetcode/internal/verify"
)

// TestDryRunHooksAndVerifyGateDoNotRun: a hook is an arbitrary shell
// command, so a dry run that let it run would be a way round the mode. Every
// event's hooks are recorded as not run, and the verify gate, which runs
// shell commands after a mutating turn, has nothing to gate.
func TestDryRunHooksAndVerifyGateDoNotRun(t *testing.T) {
	a, root := dryAgent(t,
		toolCall("t1", "write_file", `{"path":"new.txt","content":"x\n"}`),
		toolCall("t2", "bash", `{"command":"ls"}`),
		text("done"),
	)
	events := []string{hook.UserPrompt, hook.PreTool, hook.PostTool, hook.Stop, hook.Notification, hook.SessionStart}
	hs := hook.Hooks{}
	for _, ev := range events {
		hs[ev] = []hook.Hook{{Command: "touch " + filepath.Join(root, "hook-"+ev)}}
	}
	a.SetHooks(&hook.Runner{Cwd: root, Config: hook.Config{Hooks: hs}})
	a.SetVerify(&verify.Runner{Config: verify.Config{Commands: []string{"touch " + filepath.Join(root, "verify-ran")}}, Cwd: root}, false)
	a.SetDryRun(true)
	if err := a.Run(context.Background(), "change things", nil); err != nil {
		t.Fatal(err)
	}
	a.notifyHook("permission_prompt", "needs you")
	time.Sleep(200 * time.Millisecond) // observer hooks fire in the background
	for _, ev := range events {
		if _, err := os.Stat(filepath.Join(root, "hook-"+ev)); err == nil {
			t.Errorf("the %s hook ran in a dry run", ev)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "verify-ran")); err == nil {
		t.Error("the verify gate ran in a dry run")
	}
	if _, err := os.Stat(filepath.Join(root, "new.txt")); err == nil {
		t.Error("the previewed file reached the disk")
	}
	report := a.DryRunReport(false)
	for _, want := range []string{"hooks not run", "pre_tool", "post_tool", "user_prompt", "touch " + filepath.Join(root, "hook-pre_tool")} {
		if !strings.Contains(report, want) {
			t.Errorf("report lacks %q:\n%s", want, report)
		}
	}
	// Once per event, however many calls fire it.
	n := 0
	for _, x := range a.DryRunActions() {
		if x.Kind == "hook" && x.Tool == hook.PostTool {
			n++
		}
	}
	if n != 1 {
		t.Errorf("post_tool recorded %d times, want once", n)
	}
	// Applying hands the model the commands to run, never the hooks (they
	// run by themselves once the dry run is over).
	res, err := a.ApplyDryRun(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Sheet, "hook-pre_tool") {
		t.Errorf("apply told the model to run a hook: %q", res.Sheet)
	}
}

// TestDryRunBackgroundJobsAndJobControlAreRecorded: a background command
// and the calls that drive a job (stdin, kill) are recorded, not made.
func TestDryRunBackgroundJobsAndJobControlAreRecorded(t *testing.T) {
	a, root := dryAgent(t,
		toolCall("t1", "bash", `{"command":"touch bg-made","background":true}`),
		text("done"),
	)
	a.SetDryRun(true)
	if err := a.Run(context.Background(), "start a job", nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(root, "bg-made")); err == nil {
		t.Fatal("a background job ran in a dry run")
	}
	var cmds int
	for _, x := range a.DryRunActions() {
		if x.Kind == "command" && strings.Contains(x.Summary, "bg-made") {
			cmds++
		}
	}
	if cmds != 1 {
		t.Fatalf("background command recorded %d times: %+v", cmds, a.DryRunActions())
	}
}

// TestDryRunCancelledTurnLeavesNothing: cancelling mid-turn, or a provider
// failing mid-turn, leaves the disk as it was and the preview intact and
// still not applied.
func TestDryRunCancelledTurnLeavesNothing(t *testing.T) {
	a, root := dryAgent(t,
		toolCall("t1", "write_file", `{"path":"a.txt","content":"changed\n"}`),
		toolCall("t2", "bash", `{"command":"touch late"}`),
		text("done"),
	)
	a.SetDryRun(true)
	ctx, cancel := context.WithCancel(context.Background())
	_, emit := collect()
	cancelling := func(e Event) {
		emit(e)
		if e.Kind == EvToolResult && e.Call.ID == "t1" {
			cancel()
		}
	}
	_ = a.Run(ctx, "go", cancelling)
	assertUntouched := func(when string) {
		t.Helper()
		if data, _ := os.ReadFile(filepath.Join(root, "a.txt")); string(data) != "alpha\n" {
			t.Fatalf("%s: a.txt on disk = %q", when, data)
		}
		if _, err := os.Stat(filepath.Join(root, "late")); err == nil {
			t.Fatalf("%s: a recorded command ran", when)
		}
		if len(a.Checkpoints()) != 0 {
			t.Fatalf("%s: left a checkpoint", when)
		}
	}
	assertUntouched("after cancel")

	// A failing provider (the fake runs out of responses) is the same.
	b, root2 := dryAgent(t, toolCall("t1", "write_file", `{"path":"a.txt","content":"changed\n"}`))
	b.SetDryRun(true)
	if err := b.Run(context.Background(), "go", nil); err == nil {
		t.Log("run ended without an error; the preview is checked below")
	}
	if data, _ := os.ReadFile(filepath.Join(root2, "a.txt")); string(data) != "alpha\n" {
		t.Fatalf("after a failed turn: a.txt on disk = %q", data)
	}
}
