package tool

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/pipewrite"
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
	// The note names the job's log so the model reads more of it from the
	// file rather than guessing an archive id for expand_output.
	if !strings.Contains(notes[0], "full output: ") {
		t.Fatalf("note does not point at the job's log: %q", notes[0])
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

// TestJobsWriteRespectsContext is the wedged-job fix: a job that never
// reads its stdin fills the 64 KB pipe, and a pipe write has no deadline,
// so without the context bound a large stdin write from the job tool (a
// read-only tool in the parallel batch) hung the turn past cancellation.
func TestJobsWriteRespectsContext(t *testing.T) {
	jobs := &Jobs{Dir: t.TempDir()}
	job, err := jobs.Start(t.TempDir(), `sh -c 'sleep 30'`)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = jobs.Write(ctx, job.ID, strings.Repeat("x", 256<<10))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the context's deadline", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("write took %s to abandon; it ignored the context", elapsed)
	}
	// The wedged job is still killable, which also errors out the parked
	// writer.
	if err := jobs.Kill(job.ID); err != nil {
		t.Fatalf("kill: %v", err)
	}
}

func TestJobsWriteStdinAfterStart(t *testing.T) {
	jobs := &Jobs{Dir: t.TempDir()}
	job, err := jobs.Start(t.TempDir(), `sh -c 'read line; printf "got %s\n" "$line"'`)
	if err != nil {
		t.Fatal(err)
	}
	if err := jobs.Write(context.Background(), job.ID, "hello"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	j, err := jobs.Wait(ctx, job.ID)
	if err != nil || !j.Done {
		t.Fatalf("wait: %+v err=%v", j, err)
	}
	out, err := jobs.Output(job.ID, 0)
	if err != nil || !strings.Contains(out, "got hello") {
		t.Fatalf("output = %q err=%v", out, err)
	}
	res, err := JobTool{Jobs: jobs}.Run(context.Background(), json.RawMessage(`{"action":"stdin","id":1,"text":"late"}`))
	if err == nil || !strings.Contains(err.Error(), "already finished") {
		t.Fatalf("stdin after exit: %q %v", res, err)
	}
}

func TestForegroundBashStillGetsEOF(t *testing.T) {
	b := Bash{Root: t.TempDir()}
	out, err := b.Run(context.Background(), json.RawMessage(`{"command":"cat"}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "" && out != "(no output)" {
		t.Fatalf("foreground cat should see EOF, got %q", out)
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

// A background job's log has no ceiling: a dev server or a watcher writes
// to it for the whole session, and Finished asks every job that ended for
// its tail at each turn boundary. Answering that must not cost the whole
// file in memory.
func TestJobsOutputReadsOnlyTheTail(t *testing.T) {
	dir := t.TempDir()
	jobs := &Jobs{Dir: dir}
	// A log comfortably larger than the tail window.
	path := dir + "/big.log"
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Repeat("x", 99) + "\n"
	for i := 0; i < (maxOutputTail/len(line))+500; i++ {
		if _, err := f.WriteString(line); err != nil {
			t.Fatal(err)
		}
	}
	f.WriteString("the last line\n")
	f.Close()
	st, _ := os.Stat(path)
	if st.Size() <= maxOutputTail {
		t.Fatalf("the fixture log is only %d bytes", st.Size())
	}

	jobs.jobs = append(jobs.jobs, &Job{ID: 1, OutPath: path, Done: true, done: make(chan struct{})})
	out, err := jobs.Output(1, 3)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(out, "\n")
	if len(lines) != 3 || lines[2] != "the last line" {
		t.Fatalf("tail = %q", out)
	}
	// Asking for everything is still bounded by the window.
	all, err := jobs.Output(1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(all)) > maxOutputTail {
		t.Fatalf("read %d bytes of a %d-byte log", len(all), st.Size())
	}
	if !strings.HasSuffix(all, "the last line") {
		t.Fatal("the end of the log was not what came back")
	}
}

// TestAnAbandonedJobWriteEndsStdin: the context bound stopped the caller
// waiting, but the write stayed parked and completed when the job next
// drained its stdin. So the caller was told the write had failed, retried,
// and the job received both payloads — interleaved, because two writers
// were queued on one pipe. An abandoned write now ends, and if part of it
// reached the pipe the job's stdin is closed rather than spliced.
func TestAnAbandonedJobWriteEndsStdin(t *testing.T) {
	jobs := &Jobs{Dir: t.TempDir()}
	job, err := jobs.Start(t.TempDir(), `sh -c 'sleep 30'`) // never reads stdin
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = jobs.Kill(job.ID) })
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	err = jobs.Write(ctx, job.ID, strings.Repeat("a", 256<<10))
	if !errors.Is(err, pipewrite.ErrAbandoned) {
		t.Fatalf("err = %v, want an abandoned write", err)
	}
	var partial *pipewrite.PartialError
	if !errors.As(err, &partial) {
		t.Fatalf("err = %v, want it reported as partial: the pipe took some of it", err)
	}
	// The retry the caller would now make must be refused, not queued
	// behind the dead writer.
	err = jobs.Write(context.Background(), job.ID, "b\n")
	if err == nil || !strings.Contains(err.Error(), "no longer accepts stdin") {
		t.Fatalf("second write: err = %v, want a refusal", err)
	}
}

// What bash refuses is refused when it is typed into a background shell
// too; an ordinary answer still goes through.
func TestJobStdinRefusesWhatBashRefuses(t *testing.T) {
	jobs := &Jobs{Dir: t.TempDir()}
	job, err := jobs.Start(t.TempDir(), `cat`)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = jobs.Kill(job.ID) })
	jt := JobTool{Jobs: jobs}
	in := func(text string) json.RawMessage {
		b, _ := json.Marshal(map[string]any{"action": "stdin", "id": job.ID, "text": text})
		return b
	}
	if _, err := jt.Run(context.Background(), in("sudo rm -rf /")); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("err = %v, want a refusal", err)
	}
	if _, err := jt.Run(context.Background(), in("y")); err != nil {
		t.Fatalf("an ordinary answer: %v", err)
	}
}
