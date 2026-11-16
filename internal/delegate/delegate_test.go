package delegate

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/progress"
)

// fakeCLIs puts the fake claude and codex scripts first on PATH.
func fakeCLIs(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake CLIs are shell scripts")
	}
	bin, err := filepath.Abs("testdata/fakebin")
	if err != nil {
		t.Fatal(err)
	}
	// Without the fakes the real, signed-in CLIs on PATH would run (and bill).
	for _, name := range []string{"claude", "codex"} {
		if fi, err := os.Stat(filepath.Join(bin, name)); err != nil || fi.Mode()&0o111 == 0 {
			t.Fatalf("fake %s missing or not executable in %s: %v", name, bin, err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func call(t *testing.T, tl *Tool, ctx context.Context, agent, prompt string) (string, error) {
	t.Helper()
	in, _ := json.Marshal(map[string]string{"agent": agent, "prompt": prompt})
	return tl.Run(ctx, in)
}

func enabled() *Tool {
	return &Tool{Config: Config{Enabled: true}, Dir: func() string { return "" }}
}

func TestDisabledByDefault(t *testing.T) {
	fakeCLIs(t)
	tl := &Tool{Dir: func() string { return "" }}
	out, err := call(t, tl, context.Background(), "claude", "ok")
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("want a disabled error, got out=%q err=%v", out, err)
	}
}

func TestClaudeSuccessParsesAnswerCostAndProgress(t *testing.T) {
	fakeCLIs(t)
	var mu sync.Mutex
	var lines []string
	ctx := progress.With(context.Background(), func(s string) {
		mu.Lock()
		lines = append(lines, s)
		mu.Unlock()
	})
	out, err := call(t, enabled(), ctx, "claude", "ok")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "the answer is 42") {
		t.Errorf("answer missing: %q", out)
	}
	for _, want := range []string{"cost $0.0123", "115 in / 7 out tokens", "delegate claude"} {
		if !strings.Contains(out, want) {
			t.Errorf("footer lacks %q: %q", want, out)
		}
	}
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"claude: Read ", "claude: found it"} {
		if !strings.Contains(joined, want) {
			t.Errorf("progress lacks %q: %q", want, joined)
		}
	}
}

func TestClaudeDefaultsAreSafeAndRunInCwd(t *testing.T) {
	fakeCLIs(t)
	dir := t.TempDir()
	argsOut := filepath.Join(dir, "args")
	cwdOut := filepath.Join(dir, "cwd")
	t.Setenv("DELEGATE_ARGSOUT", argsOut)
	t.Setenv("DELEGATE_CWDOUT", cwdOut)
	work := t.TempDir()
	tl := &Tool{Config: Config{Enabled: true}, Dir: func() string { return work }}
	if _, err := call(t, tl, context.Background(), "claude", "ok"); err != nil {
		t.Fatal(err)
	}
	args, _ := os.ReadFile(argsOut)
	got := string(args)
	if !strings.Contains(got, "--permission-mode\ndefault\n") {
		t.Errorf("claude must default to the non-bypass permission mode, args:\n%s", got)
	}
	for _, bad := range []string{"bypassPermissions", "dangerously", "--yolo"} {
		if strings.Contains(got, bad) {
			t.Errorf("default args contain %q:\n%s", bad, got)
		}
	}
	cwd, _ := os.ReadFile(cwdOut)
	want, _ := filepath.EvalSymlinks(work)
	if have, _ := filepath.EvalSymlinks(strings.TrimSpace(string(cwd))); have != want {
		t.Errorf("ran in %q, want %q", have, want)
	}
}

func TestClaudeErrorResultIsAnError(t *testing.T) {
	fakeCLIs(t)
	_, err := call(t, enabled(), context.Background(), "claude", "error-result")
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Fatalf("want the CLI's own message, got %v", err)
	}
}

func TestNonZeroExitReportsStderr(t *testing.T) {
	fakeCLIs(t)
	_, err := call(t, enabled(), context.Background(), "claude", "fail")
	if err == nil || !strings.Contains(err.Error(), "exit status 3") || !strings.Contains(err.Error(), "boom: something broke") {
		t.Fatalf("want exit status and stderr, got %v", err)
	}
}

func TestCodexSuccessIgnoresWarningsAndSumsUsage(t *testing.T) {
	fakeCLIs(t)
	argsOut := filepath.Join(t.TempDir(), "args")
	t.Setenv("DELEGATE_ARGSOUT", argsOut)
	out, err := call(t, enabled(), context.Background(), "codex", "ok")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "pong from codex") || !strings.Contains(out, "50 in / 9 out tokens") {
		t.Errorf("unexpected result: %q", out)
	}
	if strings.Contains(out, "cost $") {
		t.Errorf("codex reports no price, so none may be shown: %q", out)
	}
	args, _ := os.ReadFile(argsOut)
	if !strings.Contains(string(args), "--sandbox\nread-only\n") {
		t.Errorf("codex must default to its read-only sandbox, args:\n%s", args)
	}
}

func TestCodexTurnFailed(t *testing.T) {
	fakeCLIs(t)
	_, err := call(t, enabled(), context.Background(), "codex", "failed")
	if err == nil || !strings.Contains(err.Error(), "quota exceeded") {
		t.Fatalf("want the turn failure, got %v", err)
	}
}

func TestEnvIsScrubbedOfProviderKeys(t *testing.T) {
	fakeCLIs(t)
	envOut := filepath.Join(t.TempDir(), "env")
	t.Setenv("DELEGATE_ENVOUT", envOut)
	t.Setenv("ZAI_API_KEY", "secret-zai")
	t.Setenv("OPENROUTER_API_KEY", "secret-or")
	t.Setenv("ANTHROPIC_API_KEY", "secret-anthropic")
	t.Setenv("OPENAI_API_KEY", "secret-openai")
	t.Setenv("DELEGATE_KEEP", "kept")
	if _, err := call(t, enabled(), context.Background(), "claude", "ok"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(envOut)
	if err != nil {
		t.Fatal(err)
	}
	env := string(b)
	for _, s := range []string{"secret-zai", "secret-or", "secret-anthropic", "secret-openai"} {
		if strings.Contains(env, s) {
			t.Errorf("child environment leaked %q", s)
		}
	}
	if !strings.Contains(env, "DELEGATE_KEEP=kept") || !strings.Contains(env, "HOME=") {
		t.Errorf("ordinary variables (HOME, so the CLI finds its login) must pass through:\n%s", env)
	}
}

func readPIDs(t *testing.T, path string) (pids []int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		b, _ := os.ReadFile(path)
		f := strings.Fields(string(b))
		if len(f) == 2 {
			for _, s := range f {
				n, err := strconv.Atoi(s)
				if err != nil {
					t.Fatal(err)
				}
				pids = append(pids, n)
			}
			return pids
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the fake CLI never wrote its pids")
	return nil
}

func assertDead(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("process %d is still alive", pid)
}

func TestTimeoutKillsTheWholeProcessGroup(t *testing.T) {
	fakeCLIs(t)
	pidfile := filepath.Join(t.TempDir(), "pids")
	t.Setenv("DELEGATE_PIDFILE", pidfile)
	tl := enabled()
	spec := tl.Config.Resolve()["claude"]
	bin, err := exec.LookPath("claude")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = tl.run(context.Background(), "claude", spec, bin, "", "hang", 400*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("want a timeout error, got %v", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Errorf("took %v to stop", time.Since(start))
	}
	for _, pid := range readPIDs(t, pidfile) { // the sleeping grandchild and the script
		assertDead(t, pid)
	}
}

func TestCancelKillsTheWholeProcessGroup(t *testing.T) {
	fakeCLIs(t)
	pidfile := filepath.Join(t.TempDir(), "pids")
	t.Setenv("DELEGATE_PIDFILE", pidfile)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := call(t, enabled(), ctx, "claude", "hang")
		done <- err
	}()
	pids := readPIDs(t, pidfile)
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("want context.Canceled, got %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	for _, pid := range pids {
		assertDead(t, pid)
	}
}

func TestMissingCommandAndUnknownAgent(t *testing.T) {
	fakeCLIs(t)
	tl := &Tool{Config: Config{Enabled: true, Agents: map[string]Spec{"ghost": {Command: "no-such-cli-xyz"}}}}
	if _, err := call(t, tl, context.Background(), "ghost", "hi"); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Errorf("want not installed, got %v", err)
	}
	if _, err := call(t, tl, context.Background(), "nope", "hi"); err == nil || !strings.Contains(err.Error(), "no delegate named") {
		t.Errorf("want unknown delegate, got %v", err)
	}
	if _, err := call(t, tl, context.Background(), "claude", "  "); err == nil {
		t.Error("an empty prompt must be refused")
	}
}

func TestResolveOverridesOnlyWhatIsSet(t *testing.T) {
	c := Config{Agents: map[string]Spec{
		"claude": {Args: []string{"-p", "{prompt}", "--permission-mode", "acceptEdits"}},
		"mine":   {Command: "my-agent"},
	}}
	r := c.Resolve()
	if r["claude"].Command != "claude" || r["claude"].Format != "claude" || r["claude"].Args[3] != "acceptEdits" {
		t.Errorf("claude override wrong: %+v", r["claude"])
	}
	if r["mine"].Format != "text" || r["codex"].Command != "codex" {
		t.Errorf("custom/builtin wrong: %+v %+v", r["mine"], r["codex"])
	}
}

func TestConfigJSONShape(t *testing.T) {
	var c Config
	doc := `{"enabled":true,"timeout_minutes":5,"agents":{"codex":{"args":["exec","--sandbox","workspace-write","{prompt}"]}}}`
	if err := json.Unmarshal([]byte(doc), &c); err != nil {
		t.Fatal(err)
	}
	if !c.Enabled || c.TimeoutMinutes != 5 || c.Resolve()["codex"].Args[2] != "workspace-write" {
		t.Errorf("config not read: %+v", c)
	}
	var zero Config
	if zero.Enabled {
		t.Error("delegates must be off unless enabled")
	}
}

func TestBuildArgsPromptPlacement(t *testing.T) {
	if got := buildArgs([]string{"-p", "{prompt}", "-x"}, "hi there"); got[1] != "hi there" || len(got) != 3 {
		t.Errorf("token not replaced in place: %v", got)
	}
	if got := buildArgs([]string{"run"}, "hi"); len(got) != 2 || got[1] != "hi" {
		t.Errorf("prompt not appended: %v", got)
	}
}
