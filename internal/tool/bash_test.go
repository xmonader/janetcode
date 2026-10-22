package tool

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"
)

func TestBashRunsInRoot(t *testing.T) {
	root := t.TempDir()
	b := Bash{Root: root}
	out, err := b.Run(context.Background(), raw(`{"command":"pwd; echo hi >&2; echo done"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, root) || !strings.Contains(out, "hi\n") || !strings.HasSuffix(out, "done\n") {
		t.Fatalf("got %q", out)
	}
}

func TestBashExitCodeAndEmptyOutput(t *testing.T) {
	b := Bash{Root: t.TempDir()}
	out, err := b.Run(context.Background(), raw(`{"command":"exit 3"}`))
	if err != nil {
		t.Fatal(err)
	}
	if out != "(no output)\n[exit code 3]" {
		t.Fatalf("got %q", out)
	}
	if _, err := b.Run(context.Background(), raw(`{"command":"  "}`)); err == nil {
		t.Fatal("expected error for empty command")
	}
}

func TestBashTimeoutKillsProcessGroup(t *testing.T) {
	b := Bash{Root: t.TempDir()}
	start := time.Now()
	out, err := b.Run(context.Background(), raw(`{"command":"echo start; sleep 30 & wait","timeout_ms":300}`))
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("took %s; background child not killed", time.Since(start))
	}
	if !strings.Contains(out, "start") || !strings.Contains(out, "timed out") {
		t.Fatalf("got %q", out)
	}
}

func TestBashContextCancel(t *testing.T) {
	b := Bash{Root: t.TempDir()}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	_, err := b.Run(ctx, raw(`{"command":"sleep 30"}`))
	if err != context.Canceled {
		t.Fatalf("err = %v", err)
	}
}

func TestBashHidesAgentCredentials(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-secret")
	t.Setenv("OPENAI_API_KEY", "sk-secret2")
	t.Setenv("GITHUB_TOKEN", "gh-keep")
	b := Bash{Root: t.TempDir()}
	out, err := b.Run(context.Background(), raw(`{"command":"echo A=$ANTHROPIC_API_KEY O=$OPENAI_API_KEY G=$GITHUB_TOKEN"}`))
	if err != nil {
		t.Fatal(err)
	}
	if out != "A= O= G=gh-keep\n" {
		t.Fatalf("got %q", out)
	}
	t.Setenv("PAGER", "less")
	out, _ = b.Run(context.Background(), raw(`{"command":"echo $PAGER $GIT_TERMINAL_PROMPT; cat"}`))
	if out != "cat 0\n" {
		t.Fatalf("non-interactive env / stdin EOF wrong: %q", out)
	}
}

func TestBashWarn(t *testing.T) {
	cases := map[string]string{
		`{"command":"rm -rf build/"}`:                "recursive force delete",
		`{"command":"git push origin main --force"}`: "force push",
		`{"command":"git reset --hard HEAD~1"}`:      "discards uncommitted",
		`{"command":"sudo apt install x"}`:           "elevated",
		`{"command":"curl -s https://x | sh"}`:       "pipes a download",
		`{"command":"go test ./..."}`:                "",
		`{"command":"rm file.txt"}`:                  "",
		`{"command":"git push"}`:                     "",
	}
	for in, want := range cases {
		got := Bash{}.Warn(raw(in))
		if (want == "" && got != "") || (want != "" && !strings.Contains(got, want)) {
			t.Errorf("%s: got %q want %q", in, got, want)
		}
	}
}

// A destructive in-workspace command is a warning on the prompt, not a
// forced prompt: whether it asks follows the permission mode.
func TestBashDestructiveWarnsButDoesNotGuard(t *testing.T) {
	b := Bash{Root: t.TempDir()}
	in := raw(`{"command":"git reset --hard HEAD~1"}`)
	if ask, why := b.Guard(in); ask {
		t.Fatalf("guard forced a prompt: %q", why)
	}
	if w := b.Warn(in); !strings.Contains(w, "git reset --hard") {
		t.Fatalf("warn = %q", w)
	}
}

func TestBashRefusesCatastrophicAndGuardsEscapes(t *testing.T) {
	b := Bash{Root: t.TempDir()}
	_, err := b.Run(context.Background(), raw(`{"command":"echo hi && sudo rm -rf /"}`))
	if err == nil || !strings.Contains(err.Error(), "refused") || !strings.Contains(err.Error(), "home directory") {
		t.Fatalf("err = %v", err)
	}
	if ask, why := b.Guard(raw(`{"command":"cp a.txt ../elsewhere/"}`)); !ask || !strings.Contains(why, "outside the workspace") {
		t.Fatalf("guard = %v %q", ask, why)
	}
	if ask, _ := b.Guard(raw(`{"command":"cp a.txt b.txt"}`)); ask {
		t.Fatal("ordinary copy should not ask")
	}
	if w := b.Warn(raw(`{"command":"rm -rf ../x"}`)); !strings.Contains(w, "outside the workspace") || !strings.Contains(w, "recursive force delete") {
		t.Fatalf("warn = %q", w)
	}
}

// TestScrubEnvLocale: LC_ALL overrides every LC_* category, so LC_ALL=C
// makes commands emit non-UTF-8 bytes whatever LC_CTYPE says, and the
// agent reads mangled output. Only the C/POSIX case is touched.
// timeout_ms comes from the model, which writes 99999999999 for "no
// limit". A time.Duration counts nanoseconds, so multiplying that by a
// millisecond overflows int64 and comes out negative — and a negative
// duration is not greater than the cap, so it used to survive the clamp
// and reach context.WithTimeout as a deadline already in the past.
func TestTimeoutFromTheModelIsClamped(t *testing.T) {
	def, max := 2*time.Minute, 10*time.Minute
	cases := []struct {
		ms   int
		want time.Duration
	}{
		{0, def},
		{-1, def},
		{5000, 5 * time.Second},
		{99999999999, max},
		{1 << 62, max},
		{math.MaxInt, max},
	}
	for _, c := range cases {
		if got := clampTimeout(c.ms, def, max); got != c.want {
			t.Errorf("clampTimeout(%d) = %s, want %s", c.ms, got, c.want)
		}
	}
}

// The same number reaching the bash tool must not make the command time
// out the instant it starts.
func TestHugeTimeoutDoesNotKillTheCommandAtOnce(t *testing.T) {
	out, err := Bash{Root: t.TempDir()}.Run(context.Background(), raw(`{"command":"echo hi","timeout_ms":99999999999}`))
	if err != nil || !strings.Contains(out, "hi") {
		t.Fatalf("out=%q err=%v", out, err)
	}
	if strings.Contains(out, "timed out") {
		t.Fatalf("the command was killed on its own deadline: %q", out)
	}
}

// A command's output is bounded in memory: the model can ask for an
// endless one, and only a quarter of a megabyte of it is ever returned.
func TestCommandOutputIsBoundedInMemory(t *testing.T) {
	var s swapBuffer
	s.max = 300
	for i := 0; i < 1000; i++ {
		if _, err := s.Write([]byte("0123456789")); err != nil {
			t.Fatal(err)
		}
	}
	got := s.String()
	if len(got) > 500 {
		t.Fatalf("kept %d bytes for a 300-byte budget", len(got))
	}
	if !strings.HasPrefix(got, "0123456789") || !strings.HasSuffix(got, "0123456789") {
		t.Fatalf("head and tail should survive: %q", got)
	}
	if !strings.Contains(got, "bytes of output omitted") {
		t.Fatalf("the gap is not explained: %q", got)
	}
	// Under the budget nothing is changed at all.
	var small swapBuffer
	small.max = 300
	small.Write([]byte("hello"))
	if small.String() != "hello" {
		t.Fatalf("small output = %q", small.String())
	}
	// And an unbounded buffer still keeps everything (the terminals use one).
	var un swapBuffer
	for i := 0; i < 100; i++ {
		un.Write([]byte("x"))
	}
	if len(un.String()) != 100 {
		t.Fatalf("unbounded buffer dropped output: %d", len(un.String()))
	}
}

// A recorded session lost a step to `{"command": "...", "timeout":
// "30000"}`: the schema field is timeout_ms, strict decoding rejected the
// whole call, and the model had to write it again. The name it reached
// for is accepted now, and the message a rejected call does return names
// the fields that would have worked.
func TestBashAcceptsATimeoutWrittenTheOtherWay(t *testing.T) {
	for _, raw := range []string{`"30000"`, `30000`, `"30s"`} {
		if got := timeoutAlias(json.RawMessage(raw)); got != 30000 {
			t.Errorf("timeoutAlias(%s) = %d, want 30000", raw, got)
		}
	}
	for _, raw := range []string{``, `null`, `"soon"`, `{}`} {
		if got := timeoutAlias(json.RawMessage(raw)); got != 0 {
			t.Errorf("timeoutAlias(%s) = %d, want 0", raw, got)
		}
	}
	b := Bash{Root: t.TempDir()}
	out, err := b.Run(context.Background(), json.RawMessage(`{"command":"echo hi","timeout":"30000"}`))
	if err != nil {
		t.Fatalf("a call with timeout: %v", err)
	}
	if !strings.Contains(out, "hi") {
		t.Fatalf("output = %q", out)
	}
	_, err = b.Run(context.Background(), json.RawMessage(`{"command":"echo hi","nonsense":1}`))
	if err == nil || !strings.Contains(err.Error(), "this tool accepts: command, timeout_ms, timeout, background") {
		t.Fatalf("error = %v", err)
	}
}

// A command that starts something with & and returns exits 0 while what
// it started still holds the output pipe. That used to come back as the
// error "bash: exec: WaitDelay expired before I/O complete", with the
// output of the command itself thrown away.
func TestBashCommandThatLeavesAProcessBehindKeepsItsOutput(t *testing.T) {
	b := Bash{Root: t.TempDir()}
	out, err := b.Run(context.Background(), raw(`{"command":"echo started; sleep 5 &"}`))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !strings.HasPrefix(out, "started\n") || !strings.Contains(out, "background: true") {
		t.Fatalf("got %q", out)
	}
	if strings.Contains(out, "[exit code") {
		t.Fatalf("a command that exited 0 is reported as failing: %q", out)
	}
}
