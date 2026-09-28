package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// A long foreground command can be handed to the jobs registry while it
// runs: it is neither waited out nor killed.
func TestForegroundCommandCanBePromoted(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell")
	}
	dir := t.TempDir()
	jobs := &Jobs{Dir: t.TempDir()}
	defer jobs.KillAll()
	run := &Running{}
	b := Bash{Root: dir, Jobs: jobs, Running: run}

	type res struct {
		out string
		err error
	}
	ch := make(chan res, 1)
	go func() {
		in, _ := json.Marshal(map[string]any{"command": "echo start; sleep 30", "timeout_ms": 120000})
		out, err := b.Run(context.Background(), in)
		ch <- res{out, err}
	}()

	deadline := time.Now().Add(5 * time.Second)
	for len(run.List()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the command never registered as promotable")
		}
		time.Sleep(10 * time.Millisecond)
	}
	live := run.List()
	if live[0].Command == "" || live[0].Pid == 0 {
		t.Fatalf("foreground entry = %+v", live[0])
	}
	job, err := run.PromoteLatest()
	if err != nil {
		t.Fatal(err)
	}
	if job != 1 {
		t.Fatalf("promoted to job %d, want 1: the caller has to be able to name the job", job)
	}
	r := <-ch
	if r.err != nil {
		t.Fatal(r.err)
	}
	if !strings.Contains(r.out, "Moved to the background as job") {
		t.Fatalf("result = %q", r.out)
	}
	list := jobs.List()
	if len(list) != 1 || list[0].Done {
		t.Fatalf("jobs = %+v", list)
	}
	// The output from before the promotion is in the job's log.
	deadline = time.Now().Add(5 * time.Second)
	for {
		out, err := jobs.Output(list[0].ID, 0)
		if err == nil && strings.Contains(out, "start") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("output before the promotion was lost: %q %v", out, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := jobs.Kill(list[0].ID); err != nil {
		t.Fatal(err)
	}
	if len(run.List()) != 0 {
		t.Fatal("the promoted command is still listed as foreground")
	}
}

// Promotion has to survive the tool call returning. The command is started
// on the turn's context, and os/exec kills a command as soon as that
// context ends — which is the instant Run returns with "moved to the
// background".
func TestPromotedCommandKeepsRunning(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell")
	}
	dir := t.TempDir()
	jobs := &Jobs{Dir: t.TempDir()}
	defer jobs.KillAll()
	run := &Running{}
	b := Bash{Root: dir, Jobs: jobs, Running: run}
	marker := filepath.Join(dir, "done")

	done := make(chan struct{})
	go func() {
		defer close(done)
		in, _ := json.Marshal(map[string]any{
			"command":    "echo start; sleep 0.4; echo after-promotion; touch " + marker + "; sleep 30",
			"timeout_ms": 120000,
		})
		b.Run(context.Background(), in)
	}()

	deadline := time.Now().Add(5 * time.Second)
	for len(run.List()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the command never registered as promotable")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := run.PromoteLatest(); err != nil {
		t.Fatal(err)
	}
	<-done

	list := jobs.List()
	if len(list) != 1 {
		t.Fatalf("jobs = %+v", list)
	}
	// Work the command had not reached when it was promoted must still
	// happen: a job killed by the departing tool call never gets here.
	deadline = time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if j := jobs.List()[0]; j.Done {
			t.Fatalf("the promoted job died on its own with exit %d", j.Exit)
		}
		if time.Now().After(deadline) {
			t.Fatal("the promoted job never did the work it was promoted to finish")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := jobs.Kill(list[0].ID); err != nil {
		t.Fatal(err)
	}
}

func TestPromoteErrors(t *testing.T) {
	var nilRun *Running
	if _, err := nilRun.PromoteLatest(); err == nil {
		t.Fatal("a session without promotion should say so")
	}
	run := &Running{}
	if _, err := run.Promote(7); err == nil {
		t.Fatal("an unknown id should be an error")
	}
	if _, err := run.PromoteLatest(); err == nil {
		t.Fatal("nothing running should be an error")
	}
}

// Without a Running registry nothing changes: the command runs and returns.
func TestBashStillWorksWithoutPromotion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell")
	}
	out, err := Bash{Root: t.TempDir()}.Run(context.Background(), json.RawMessage(`{"command":"echo hi"}`))
	if err != nil || !strings.Contains(out, "hi") {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestSedInPlaceIsPreviewedAsADiff(t *testing.T) {
	if !sedHasSandbox() {
		t.Skip("no sed with --sandbox")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "a.txt")
	os.WriteFile(p, []byte("alpha\nbeta\ngamma\n"), 0o644)
	b := Bash{Root: dir}
	in, _ := json.Marshal(map[string]string{"command": "sed -i 's/beta/BETA/' a.txt"})
	out, err := b.Preview(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "would rewrite a.txt") || !strings.Contains(out, "-beta") || !strings.Contains(out, "+BETA") {
		t.Fatalf("preview = %q", out)
	}
	// Previewing must not have touched the file.
	if data, _ := os.ReadFile(p); string(data) != "alpha\nbeta\ngamma\n" {
		t.Fatalf("the preview edited the file: %q", data)
	}
	// sed without -i writes nothing, so there is nothing to show.
	in, _ = json.Marshal(map[string]string{"command": "sed 's/beta/BETA/' a.txt"})
	if out, _ := b.Preview(context.Background(), in); out != "" {
		t.Fatalf("a read-only sed should have no preview: %q", out)
	}
	// An ordinary command has no preview either.
	in, _ = json.Marshal(map[string]string{"command": "go test ./..."})
	if out, _ := b.Preview(context.Background(), in); out != "" {
		t.Fatalf("preview = %q", out)
	}
}

// The preview runs before the user has been asked, so it must not be a
// way to run the command early. GNU sed's e command executes its
// argument as a shell command and w writes a file: a `sed -i` whose
// script used either did so the moment the call was described, in any
// mode, approved or not.
func TestSedPreviewRunsNothingAndWritesNothing(t *testing.T) {
	if !sedHasSandbox() {
		t.Skip("sed has no --sandbox; the preview is not run at all")
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("alpha\n"), 0o644)
	b := Bash{Root: dir}
	for _, script := range []string{"1e touch ran", "1w wrote"} {
		in, _ := json.Marshal(map[string]string{"command": "sed -i '" + script + "' a.txt"})
		b.Preview(context.Background(), in)
	}
	for _, f := range []string{"ran", "wrote"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
			t.Errorf("previewing a sed -i created %s before anyone approved it", f)
		}
	}
}

func TestSplitSedArgs(t *testing.T) {
	args, files, in := splitSedArgs([]string{"-i", "s/a/b/", "x.go", "y.go"})
	if !in || len(files) != 2 || len(args) != 1 || args[0] != "s/a/b/" {
		t.Fatalf("args=%v files=%v in=%v", args, files, in)
	}
	if _, _, in := splitSedArgs([]string{"s/a/b/", "x.go"}); in {
		t.Fatal("no -i, not in place")
	}
	args, files, in = splitSedArgs([]string{"-i.bak", "-e", "s/a/b/", "x.go"})
	if !in || len(files) != 1 || strings.Join(args, " ") != "-e s/a/b/" {
		t.Fatalf("args=%v files=%v", args, files)
	}
	args, files, in = splitSedArgs([]string{"--in-place", "-n", "1,3p", "x.go"})
	if !in || len(files) != 1 || strings.Join(args, " ") != "-n 1,3p" {
		t.Fatalf("args=%v files=%v", args, files)
	}
}
