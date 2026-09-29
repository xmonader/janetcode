//go:build unix

package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func bashRun(t *testing.T, b Bash, input map[string]any) string {
	t.Helper()
	raw, _ := json.Marshal(input)
	out, err := b.Run(context.Background(), raw)
	if err != nil {
		t.Fatalf("bash %v: %v", input, err)
	}
	return out
}

func jobRun(t *testing.T, jobs *Jobs, input string) string {
	t.Helper()
	out, err := JobTool{Jobs: jobs}.Run(context.Background(), json.RawMessage(input))
	if err != nil {
		t.Fatalf("job %s: %v", input, err)
	}
	return out
}

func TestBashStdinIsFedToTheCommand(t *testing.T) {
	out := bashRun(t, Bash{Root: t.TempDir()}, map[string]any{"command": `read a; read b; echo "$b-$a"`, "stdin": "x\ny\n"})
	if !strings.Contains(out, "y-x") {
		t.Fatalf("got %q", out)
	}
}

// A model-run command must not share janetcode's terminal: one that
// opened /dev/tty to ask for a password used to stop on SIGTTIN and sit
// there until the timeout.
func TestBashCommandsHaveNoControllingTerminal(t *testing.T) {
	out := bashRun(t, Bash{Root: t.TempDir()}, map[string]any{"command": `test "$(ps -o sid= -p $$ | tr -d ' ')" = "$$" && echo own-session; (exec </dev/tty) 2>/dev/null || echo no-tty`})
	if !strings.Contains(out, "own-session") || !strings.Contains(out, "no-tty") {
		t.Fatalf("command shares a session or has a terminal: %q", out)
	}
	jobs := &Jobs{Dir: t.TempDir()}
	defer jobs.KillAll()
	job, err := jobs.Start(t.TempDir(), `test "$(ps -o sid= -p $$ | tr -d ' ')" = "$$" && echo own-session`)
	if err != nil {
		t.Fatal(err)
	}
	if j, _ := jobs.Wait(context.Background(), job.ID); j.Exit != 0 {
		o, _ := jobs.Output(job.ID, 0)
		t.Fatalf("background job shares a session: %q", o)
	}
}

func needsProc(t *testing.T) {
	if runtime.GOOS != "linux" || !stdinWatchable() {
		t.Skip("reads of stdin are only seen through Linux /proc")
	}
}

var jobIDRe = regexp.MustCompile(`background job (\d+)`)

func TestBashPromptingCommandBecomesAJobToAnswer(t *testing.T) {
	needsProc(t)
	jobs := &Jobs{Dir: t.TempDir()}
	defer jobs.KillAll()
	b := Bash{Root: t.TempDir(), Jobs: jobs}
	start := time.Now()
	out := bashRun(t, b, map[string]any{"command": `echo working; printf 'Name? '; read n; printf 'Age? '; read a; echo "$n is $a"`})
	if time.Since(start) > 10*time.Second {
		t.Fatalf("took %s to notice the prompt", time.Since(start))
	}
	if !strings.Contains(out, "waiting for input") || !strings.Contains(out, "Name?") {
		t.Fatalf("not reported as waiting: %q", out)
	}
	m := jobIDRe.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no job id in %q", out)
	}
	id, _ := strconv.Atoi(m[1])
	reply := jobRun(t, jobs, fmt.Sprintf(`{"action":"stdin","id":%d,"text":"Ann"}`, id))
	if !strings.Contains(reply, "Age?") {
		t.Fatalf("answer did not bring the next question: %q", reply)
	}
	reply = jobRun(t, jobs, fmt.Sprintf(`{"action":"stdin","id":%d,"text":"30"}`, id))
	if !strings.Contains(reply, "Ann is 30") {
		t.Fatalf("second answer: %q", reply)
	}
	j, _ := jobs.Wait(context.Background(), id)
	if j.Exit != 0 {
		t.Fatalf("job exit %d", j.Exit)
	}
}

// A command that reads stdin without asking anything — a filter — still
// gets its end-of-file, promptly, and is not turned into a job.
func TestBashReadWithoutPromptGetsEOF(t *testing.T) {
	needsProc(t)
	jobs := &Jobs{Dir: t.TempDir()}
	defer jobs.KillAll()
	start := time.Now()
	out := bashRun(t, Bash{Root: t.TempDir(), Jobs: jobs}, map[string]any{"command": `cat; wc -l; echo done`})
	if !strings.Contains(out, "done") || strings.Contains(out, "waiting") {
		t.Fatalf("got %q", out)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("EOF took %s", d)
	}
	if len(jobs.List()) != 0 {
		t.Fatal("a filter became a job")
	}
}

// With nobody to answer (no jobs registry), a prompt gets EOF and the
// result says why the command failed.
func TestBashPromptWithoutJobsSaysItReadStdin(t *testing.T) {
	needsProc(t)
	out := bashRun(t, Bash{Root: t.TempDir()}, map[string]any{"command": `printf 'Continue? [y/N] '; read x`})
	if !strings.Contains(out, "read its standard input") {
		t.Fatalf("got %q", out)
	}
}

func TestInteractiveJobRunsOnATerminal(t *testing.T) {
	jobs := &Jobs{Dir: t.TempDir()}
	defer jobs.KillAll()
	b := Bash{Root: t.TempDir(), Jobs: jobs}
	// The secret is read from /dev/tty with echo off, the way ssh-keygen
	// and sudo ask; only a job on a terminal can answer that.
	out := bashRun(t, b, map[string]any{"interactive": true, "command": `test -t 0 && echo on-a-terminal; stty -echo; printf 'Secret: '; read s </dev/tty; stty echo; echo; echo "got ${#s} chars"; printf 'Again? '; read r; echo "again=$r"`})
	if !strings.Contains(out, "interactive job") || !strings.Contains(out, "on-a-terminal") || !strings.Contains(out, "Secret:") {
		t.Fatalf("start: %q", out)
	}
	id, _ := strconv.Atoi(regexp.MustCompile(`interactive job (\d+)`).FindStringSubmatch(out)[1])
	reply := jobRun(t, jobs, fmt.Sprintf(`{"action":"stdin","id":%d,"text":"hunter2"}`, id))
	if !strings.Contains(reply, "got 7 chars") || strings.Contains(reply, "hunter2") {
		t.Fatalf("secret answer: %q", reply)
	}
	reply = jobRun(t, jobs, fmt.Sprintf(`{"action":"stdin","id":%d,"text":""}`, id))
	if !strings.Contains(reply, "again=") {
		t.Fatalf("enter: %q", reply)
	}
	j, _ := jobs.Wait(context.Background(), id)
	o, _ := jobs.Output(id, 0)
	if j.Exit != 0 || strings.Contains(o, "\r") || strings.Contains(o, "\x1b") {
		t.Fatalf("exit %d, log %q", j.Exit, o)
	}
}

func TestJobStdinEnterAndEOF(t *testing.T) {
	jobs := &Jobs{Dir: t.TempDir()}
	defer jobs.KillAll()
	job, err := jobs.Start(t.TempDir(), `read x; echo "[$x]"; cat; echo end`)
	if err != nil {
		t.Fatal(err)
	}
	if r := jobRun(t, jobs, fmt.Sprintf(`{"action":"stdin","id":%d,"text":""}`, job.ID)); !strings.Contains(r, "[]") {
		t.Fatalf("empty text did not press Enter: %q", r)
	}
	r := jobRun(t, jobs, fmt.Sprintf(`{"action":"stdin","id":%d,"text":"last line","eof":true}`, job.ID))
	if !strings.Contains(r, "last line") || !strings.Contains(r, "end") {
		t.Fatalf("eof: %q", r)
	}
	if _, err := (JobTool{Jobs: jobs}).Run(context.Background(), json.RawMessage(fmt.Sprintf(`{"action":"stdin","id":%d}`, job.ID))); err == nil {
		t.Fatal("stdin with neither text nor eof was accepted")
	}
}

func TestTermText(t *testing.T) {
	for in, want := range map[string]string{
		"a\r\nb\r\n":             "a\nb\n",
		"\x1b[31mred\x1b[0m\r\n": "red\n",
		"10%\r50%\r100%\r\ndone": "100%\ndone",
		"plain":                  "plain",
	} {
		if got := termText(in); got != want {
			t.Errorf("termText(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLooksLikePrompt(t *testing.T) {
	for _, c := range []struct {
		out    string
		strong bool
		want   bool
	}{
		{"Name? ", false, true},
		{"package name: (demo) ", false, true},
		{"Overwrite? [y/N] ", false, true},
		{"Building: ", false, true},
		{"compiling 12 files", false, false},
		{"done\n", false, false},
		{"Enter your name:\n", true, true},
		{"loaded 3 items\n", true, false},
		{"> ", true, true},
		{"\x1b[1mproceed?\x1b[0m ", false, true},
	} {
		if got := looksLikePrompt(c.out, c.strong); got != c.want {
			t.Errorf("looksLikePrompt(%q, %v) = %v", c.out, c.strong, got)
		}
	}
}
