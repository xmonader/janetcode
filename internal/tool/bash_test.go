package tool

import (
	"context"
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
func TestScrubEnvLocale(t *testing.T) {
	get := func(env []string, key string) (string, bool) {
		val, ok := "", false
		for _, kv := range env { // last wins, as exec does
			if n, v, _ := strings.Cut(kv, "="); n == key {
				val, ok = v, true
			}
		}
		return val, ok
	}
	got := scrubEnv([]string{"LC_ALL=C", "LC_CTYPE=en_US.UTF-8", "PATH=/bin"})
	if _, ok := get(got, "LC_ALL"); ok {
		t.Error("LC_ALL=C should be dropped; it defeats LC_CTYPE")
	}
	if v, _ := get(got, "LC_CTYPE"); v != "C.UTF-8" {
		t.Errorf("LC_CTYPE = %q, want C.UTF-8", v)
	}
	if v, _ := get(got, "PATH"); v != "/bin" {
		t.Errorf("PATH was disturbed: %q", v)
	}
	// A real locale is left exactly as the user set it.
	got = scrubEnv([]string{"LC_ALL=de_DE.UTF-8", "LC_CTYPE=de_DE.UTF-8"})
	if v, ok := get(got, "LC_ALL"); !ok || v != "de_DE.UTF-8" {
		t.Errorf("a real LC_ALL must survive, got %q %v", v, ok)
	}
}
