package tool

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestJobsStartWaitOutputAndFinished(t *testing.T) {
	root := t.TempDir()
	jobs := &Jobs{Dir: t.TempDir()}
	b := Bash{Root: root, Jobs: jobs, Cache: &ShellCache{}}
	out, err := b.Run(context.Background(), json.RawMessage(`{"command":"echo start; sleep 0.3; echo done; exit 3","background":true}`))
	if err != nil || !strings.HasPrefix(out, "Started background job 1 (pid ") {
		t.Fatalf("out=%q err=%v", out, err)
	}
	if notes := jobs.Finished(); len(notes) != 0 {
		t.Fatalf("finished too early: %q", notes)
	}
	jt := JobTool{Jobs: jobs}
	list, _ := jt.Run(context.Background(), json.RawMessage(`{"action":"list"}`))
	if !strings.Contains(list, "job 1 running") {
		t.Fatalf("list = %q", list)
	}
	res, err := jt.Run(context.Background(), json.RawMessage(`{"action":"wait","id":1,"timeout_ms":5000}`))
	if err != nil || !strings.Contains(res, "job 1 finished with exit 3") || !strings.Contains(res, "done") {
		t.Fatalf("wait = %q err=%v", res, err)
	}
	notes := jobs.Finished()
	if len(notes) != 1 || !strings.Contains(notes[0], "exit 3") || !strings.Contains(notes[0], "last output:\nstart\ndone") {
		t.Fatalf("notes = %q", notes)
	}
	if again := jobs.Finished(); len(again) != 0 {
		t.Fatalf("reported twice: %q", again)
	}
	res, _ = jt.Run(context.Background(), json.RawMessage(`{"action":"output","id":1,"lines":1}`))
	if !strings.HasSuffix(res, "\ndone") {
		t.Fatalf("output tail = %q", res)
	}
	if _, err := jt.Run(context.Background(), json.RawMessage(`{"action":"kill","id":1}`)); err == nil || !strings.Contains(err.Error(), "already finished") {
		t.Fatalf("kill finished job: %v", err)
	}
	if _, err := jt.Run(context.Background(), json.RawMessage(`{"action":"output","id":9}`)); err == nil {
		t.Fatal("unknown job accepted")
	}
}

func TestJobsKillStopsTheProcessGroup(t *testing.T) {
	jobs := &Jobs{Dir: t.TempDir()}
	job, err := jobs.Start(t.TempDir(), "sleep 30 & sleep 30; echo never")
	if err != nil {
		t.Fatal(err)
	}
	res, _ := JobTool{Jobs: jobs}.Run(context.Background(), json.RawMessage(`{"action":"wait","id":1,"timeout_ms":100}`))
	if !strings.Contains(res, "still running after") {
		t.Fatalf("wait = %q", res)
	}
	if err := jobs.Kill(job.ID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	j, err := jobs.Wait(ctx, job.ID)
	if err != nil || !j.Done || j.Exit == 0 {
		t.Fatalf("after kill: %+v err=%v", j, err)
	}
	if data, _ := os.ReadFile(j.OutPath); strings.Contains(string(data), "never") {
		t.Fatal("job kept running after kill")
	}
	jobs.KillAll() // nothing left; must not panic
	if b := (Bash{Root: t.TempDir()}); true {
		if _, err := b.Run(context.Background(), json.RawMessage(`{"command":"true","background":true}`)); err == nil || !strings.Contains(err.Error(), "not available") {
			t.Fatalf("background without Jobs: %v", err)
		}
	}
}
