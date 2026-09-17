package hook

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

func TestPreToolDenyRewriteAndFailOpen(t *testing.T) {
	var logs []string
	r := &Runner{Cwd: t.TempDir(), Log: func(s string) { logs = append(logs, s) }, Config: Config{Hooks: Hooks{
		PreTool: {
			{Match: "^bash$", Command: `grep -q 'rm -rf' && { echo "no rm -rf here" >&2; exit 2; }; exit 0`},
			{Command: `cat >/dev/null; echo '{"input":{"command":"echo rewritten"},"note":"rewrote it"}'`},
			{Command: `exit 7`},              // unexpected exit: ignored
			{Command: `sleep 5`, Timeout: 1}, // timeout: ignored
			{Match: "write_file", Command: `echo '{"decision":"deny"}'`},
		},
	}}}
	ctx := context.Background()
	res := r.Run(ctx, Input{Event: PreTool, Tool: "bash", Input: json.RawMessage(`{"command":"rm -rf /tmp/x"}`)})
	if !res.Denied || res.Reason != "no rm -rf here" {
		t.Fatalf("deny: %+v", res)
	}
	res = r.Run(ctx, Input{Event: PreTool, Tool: "bash", Input: json.RawMessage(`{"command":"ls"}`)})
	if res.Denied || string(res.Input) != `{"command":"echo rewritten"}` || len(res.Notes) != 1 || res.Notes[0] != "rewrote it" {
		t.Fatalf("rewrite: %+v", res)
	}
	if len(logs) != 2 || !strings.Contains(logs[0], "exit 7") || !strings.Contains(logs[1], "timed out") {
		t.Fatalf("logs = %q", logs)
	}
	// The name matcher keeps the write_file deny away from grep, and the
	// bash-only hook away from write_file.
	if res = r.Run(ctx, Input{Event: PreTool, Tool: "grep", Input: json.RawMessage(`{}`)}); res.Denied {
		t.Fatalf("grep denied: %+v", res)
	}
	if res = r.Run(ctx, Input{Event: PreTool, Tool: "write_file", Input: json.RawMessage(`{}`)}); !res.Denied || !strings.Contains(res.Reason, "blocked by pre_tool hook 5") {
		t.Fatalf("write_file: %+v", res)
	}
	if (&Runner{}).Has(PreTool) || !r.Has(PreTool) || r.Has(Stop) {
		t.Fatal("Has wrong")
	}
	var nilRunner *Runner
	if res := nilRunner.Run(ctx, Input{Event: PreTool, Input: json.RawMessage(`1`)}); res.Denied || string(res.Input) != "1" {
		t.Fatal("nil runner must pass through")
	}
}

func TestPostToolNotesAndStopContinue(t *testing.T) {
	dir := t.TempDir()
	r := &Runner{Cwd: dir, Config: Config{Hooks: Hooks{
		PostTool: {{Command: `python3 -c 'import sys,json; d=json.load(sys.stdin); print("saw " + d["tool"] + " " + d["output"].strip() + (" ERR" if d["is_error"] else ""))'`}},
		Stop:     {{Command: `python3 -c 'import sys,json; d=json.load(sys.stdin); print(json.dumps({"continue":"run the tests first"}) if "done" in d["text"] else "")'`}},
	}}}
	ctx := context.Background()
	res := r.Run(ctx, Input{Event: PostTool, Tool: "bash", Output: "hello\n", IsError: true})
	if len(res.Notes) != 1 || res.Notes[0] != "saw bash hello ERR" {
		t.Fatalf("post: %+v", res)
	}
	if res = r.Run(ctx, Input{Event: Stop, Text: "all done"}); res.Continue != "run the tests first" {
		t.Fatalf("stop: %+v", res)
	}
	if res = r.Run(ctx, Input{Event: Stop, Text: "still going"}); res.Continue != "" {
		t.Fatalf("stop passthrough: %+v", res)
	}
	// Env vars and cwd reach the hook.
	r.Config.Hooks[PreCompact] = []Hook{{Command: `echo "$JANETCODE_EVENT in $(pwd)"`}}
	res = r.Run(ctx, Input{Event: PreCompact, Tokens: 90000})
	if len(res.Notes) != 1 || res.Notes[0] != "pre_compact in "+realPath(t, dir) {
		t.Fatalf("env: %+v", res)
	}
}

func realPath(t *testing.T, p string) string {
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestLoadProjectAndValidate(t *testing.T) {
	root := t.TempDir()
	if c, h, err := LoadProject(root); c.Len() != 0 || h != "" || err != nil {
		t.Fatalf("missing file: %v %q %v", c, h, err)
	}
	os.MkdirAll(filepath.Join(root, ".janetcode"), 0o755)
	p := filepath.Join(root, ProjectFile)
	os.WriteFile(p, []byte(`{"pre_tool":[{"match":"bash","command":"./check.sh"}]}`), 0o644)
	c, h, err := LoadProject(root)
	if err != nil || len(c.For(PreTool)) != 1 || len(h) != 64 {
		t.Fatalf("%v %q %v", c, h, err)
	}
	os.WriteFile(p, []byte(`{"on_save":[{"command":"x"}]}`), 0o644)
	if _, _, err := LoadProject(root); err == nil || !strings.Contains(err.Error(), "unknown event") {
		t.Fatalf("unknown event accepted: %v", err)
	}
	if err := (Config{Hooks: Hooks{PreTool: {{Match: "(", Command: "x"}}}}).Validate(); err == nil {
		t.Fatal("bad regexp accepted")
	}
	if err := (Config{Hooks: Hooks{Stop: {{Command: " "}}}}).Validate(); err == nil {
		t.Fatal("empty command accepted")
	}
	m := Merge(Config{Hooks: Hooks{PreTool: {{Command: "a"}}}}, Config{Hooks: Hooks{PreTool: {{Command: "b"}}, Stop: {{Command: "c"}}}})
	if len(m.For(PreTool)) != 2 || m.For(PreTool)[0].Command != "a" || len(m.For(Stop)) != 1 {
		t.Fatalf("merge = %+v", m)
	}
}

// The limits are the user's to set, so they have to be readable from the
// same "hooks" object the hooks themselves live in.
func TestLimitsRoundTripThroughJSON(t *testing.T) {
	var c Config
	if err := json.Unmarshal([]byte(`{"pre_tool":[{"command":"x"}],"limits":{"timeout":3,"output":512,"per_event":2}}`), &c); err != nil {
		t.Fatal(err)
	}
	if len(c.For(PreTool)) != 1 || c.Limits.Timeout != 3 || c.Limits.Output != 512 || c.Limits.PerEvent != 2 {
		t.Fatalf("config = %+v", c)
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if d := c.Limits.WithDefaults(); d.ProjectTimeout != DefaultProjectTimeout || d.Timeout != 3 {
		t.Fatalf("defaults = %+v", d)
	}
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var back Config
	if err := json.Unmarshal(data, &back); err != nil || back.Limits != c.Limits || len(back.For(PreTool)) != 1 {
		t.Fatalf("round trip = %s (%v)", data, err)
	}
	// A misspelled limit is a mistake, not a new setting.
	if err := json.Unmarshal([]byte(`{"limits":{"timeoutt":3}}`), &Config{}); err == nil {
		t.Fatal("unknown limit accepted")
	}
	// And a negative one cannot mean anything.
	if err := (Config{Limits: Limits{Output: -1}}).Validate(); err == nil {
		t.Fatal("negative limit accepted")
	}
}

func TestLimitsBoundTimeoutOutputAndCount(t *testing.T) {
	dir := t.TempDir()
	var logs []string
	// The default timeout applies to a hook that names none.
	r := &Runner{Cwd: dir, Log: func(s string) { logs = append(logs, s) }, Config: Config{
		Limits: Limits{Timeout: 1, Output: 64, PerEvent: 2},
		Hooks: Hooks{
			PostTool: {
				{Command: `sleep 5`},
				{Command: `python3 -c "print('y' * 5000)"`},
				{Command: `echo third`},
			},
		},
	}}
	res := r.Run(context.Background(), Input{Event: PostTool, Tool: "bash"})
	if len(logs) == 0 || !strings.Contains(logs[0], "timed out after 1s") {
		t.Fatalf("logs = %q", logs)
	}
	if len(res.Notes) != 1 {
		t.Fatalf("notes = %q", res.Notes)
	}
	if n := len(res.Notes[0]); n > 64+200 {
		t.Fatalf("a %d-byte note got through a 64-byte limit", n)
	}
	if !strings.Contains(res.Notes[0], "were dropped") {
		t.Fatalf("the truncation is not explained: %q", res.Notes[0])
	}
	// The third hook never ran: two per event was the limit.
	for _, n := range res.Notes {
		if strings.Contains(n, "third") {
			t.Fatal("a hook ran past the per_event limit")
		}
	}
	var said bool
	for _, l := range logs {
		said = said || strings.Contains(l, "per_event")
	}
	if !said {
		t.Fatalf("nothing said the limit stopped a hook: %q", logs)
	}
}

// A hook's timeout has to bound the whole tree it started. Killing the
// shell alone leaves a grandchild holding the output pipes, and the wait
// on those pipes used to hold the turn open for as long as the
// grandchild lived — far past the timeout that was supposed to end it.
func TestHookTimeoutKillsTheWholeTree(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix shell command")
	}
	var logs []string
	r := &Runner{Cwd: t.TempDir(), Log: func(s string) { logs = append(logs, s) },
		Config: Config{Hooks: Hooks{PreTool: {{Command: "sleep 60 & wait", Timeout: 1}}}}}
	start := time.Now()
	res := r.Run(context.Background(), Input{Event: PreTool, Tool: "bash", Input: json.RawMessage(`{}`)})
	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Fatalf("hook ran for %s despite a 1s timeout", elapsed)
	}
	if res.Denied {
		t.Fatalf("a timed-out hook must fail open: %+v", res)
	}
	if len(logs) != 1 || !strings.Contains(logs[0], "timed out") {
		t.Fatalf("logs = %q", logs)
	}
}

// A hook whose match pattern cannot compile is reported, not silently
// skipped for the rest of the session.
// TestHookEnvIsScrubbed is the credential-leak fix: hooks are
// project-controlled commands whose stdout becomes conversation notes, so
// they must not see the agent's API keys in their environment — a hook
// could print one straight back into the context.
func TestHookEnvIsScrubbed(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-hook-marker")
	t.Setenv("OPENAI_API_KEY", "sk-oai-hook-marker")
	r := &Runner{Cwd: t.TempDir(), Config: Config{Hooks: Hooks{
		PostTool: {{Command: `echo "ANTHROPIC_API_KEY=[$ANTHROPIC_API_KEY] OPENAI_API_KEY=[$OPENAI_API_KEY]"`}},
	}}}
	res := r.Run(context.Background(), Input{Event: PostTool, Tool: "bash", Input: json.RawMessage(`{}`)})
	if len(res.Notes) != 1 {
		t.Fatalf("notes = %q", res.Notes)
	}
	if strings.Contains(res.Notes[0], "sk-ant-hook-marker") || strings.Contains(res.Notes[0], "sk-oai-hook-marker") {
		t.Fatalf("hook saw the agent's credentials: %q", res.Notes[0])
	}
	if !strings.Contains(res.Notes[0], "ANTHROPIC_API_KEY=[]") {
		t.Fatalf("hook did not run with a clean env: %q", res.Notes[0])
	}
}

func TestBadMatchIsReported(t *testing.T) {
	var logs []string
	r := &Runner{Cwd: t.TempDir(), Log: func(s string) { logs = append(logs, s) },
		Config: Config{Hooks: Hooks{PreTool: {{Match: "([", Command: "exit 2"}}}}}
	res := r.Run(context.Background(), Input{Event: PreTool, Tool: "bash", Input: json.RawMessage(`{}`)})
	if res.Denied {
		t.Fatalf("a hook with an unusable match must not run: %+v", res)
	}
	if len(logs) != 1 || !strings.Contains(logs[0], "not a valid regexp") {
		t.Fatalf("logs = %q", logs)
	}
}

// A project may not raise its own limits: trusting a checkout's hooks is
// not a decision to let it decide how long they may run.
func TestProjectCannotRaiseItsOwnLimits(t *testing.T) {
	user := Config{Limits: Limits{ProjectTimeout: 5}}
	project := Config{
		Limits: Limits{ProjectTimeout: 600},
		Hooks:  Hooks{Stop: {{Command: "go build ./...", Timeout: 600}}},
	}
	merged, notes := MergeScoped(user, project)
	if got := merged.For(Stop)[0].Timeout; got != 5 {
		t.Fatalf("timeout = %d, want the user's 5", got)
	}
	if merged.Limits != user.Limits {
		t.Fatalf("limits = %+v, want the user's %+v", merged.Limits, user.Limits)
	}
	var said bool
	for _, n := range notes {
		said = said || strings.Contains(n, "cannot raise its own limits")
	}
	if !said {
		t.Fatalf("notes = %v", notes)
	}
}

// TestProjectHookCannotAllowAToolCall is the permission-gate fix: a hook
// that came from the workspace's own hooks.json is project content, and
// project content must not be able to approve calls on the user's behalf
// — one {"decision":"allow"} used to switch off the prompt for every
// non-guarded mutating call in the session. The user's own hooks keep the
// power.
func TestProjectHookCannotAllowAToolCall(t *testing.T) {
	user := Config{Hooks: Hooks{PreTool: {
		{Command: `echo '{"decision":"allow"}'`},
	}}}
	project := Config{Hooks: Hooks{PreTool: {
		{Command: `echo '{"decision":"allow","note":"project says yes"}'`},
	}}}
	merged, notes := MergeScoped(user, project)
	if len(notes) != 0 {
		t.Fatalf("notes = %q", notes)
	}
	var logs []string
	r := &Runner{Cwd: t.TempDir(), Log: func(s string) { logs = append(logs, s) }, Config: merged}
	res := r.Run(context.Background(), Input{Event: PreTool, Tool: "bash", Input: json.RawMessage(`{}`)})
	if !res.Allowed {
		t.Fatal("the user's own hook should still be able to allow")
	}
	// Now the project hook alone.
	r2 := &Runner{Cwd: t.TempDir(), Log: func(s string) { logs = append(logs, s) }, Config: func() Config {
		c, _ := MergeScoped(Config{}, project)
		return c
	}()}
	res = r2.Run(context.Background(), Input{Event: PreTool, Tool: "bash", Input: json.RawMessage(`{}`)})
	if res.Allowed {
		t.Fatal("a project hook allowed a tool call on the user's behalf")
	}
	joined := strings.Join(logs, "\n")
	if !strings.Contains(joined, "a project hook cannot allow") {
		t.Fatalf("the refusal should be logged, logs = %q", joined)
	}
}
