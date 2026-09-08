package hook

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreToolDenyRewriteAndFailOpen(t *testing.T) {
	var logs []string
	r := &Runner{Cwd: t.TempDir(), Log: func(s string) { logs = append(logs, s) }, Config: Config{
		PreTool: {
			{Match: "^bash$", Command: `grep -q 'rm -rf' && { echo "no rm -rf here" >&2; exit 2; }; exit 0`},
			{Command: `cat >/dev/null; echo '{"input":{"command":"echo rewritten"},"note":"rewrote it"}'`},
			{Command: `exit 7`},              // unexpected exit: ignored
			{Command: `sleep 5`, Timeout: 1}, // timeout: ignored
			{Match: "write_file", Command: `echo '{"decision":"deny"}'`},
		},
	}}
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
	r := &Runner{Cwd: dir, Config: Config{
		PostTool: {{Command: `python3 -c 'import sys,json; d=json.load(sys.stdin); print("saw " + d["tool"] + " " + d["output"].strip() + (" ERR" if d["is_error"] else ""))'`}},
		Stop:     {{Command: `python3 -c 'import sys,json; d=json.load(sys.stdin); print(json.dumps({"continue":"run the tests first"}) if "done" in d["text"] else "")'`}},
	}}
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
	r.Config[PreCompact] = []Hook{{Command: `echo "$JANETCODE_EVENT in $(pwd)"`}}
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
	if c, h, err := LoadProject(root); c != nil || h != "" || err != nil {
		t.Fatalf("missing file: %v %q %v", c, h, err)
	}
	os.MkdirAll(filepath.Join(root, ".janetcode"), 0o755)
	p := filepath.Join(root, ProjectFile)
	os.WriteFile(p, []byte(`{"pre_tool":[{"match":"bash","command":"./check.sh"}]}`), 0o644)
	c, h, err := LoadProject(root)
	if err != nil || len(c[PreTool]) != 1 || len(h) != 64 {
		t.Fatalf("%v %q %v", c, h, err)
	}
	os.WriteFile(p, []byte(`{"on_save":[{"command":"x"}]}`), 0o644)
	if _, _, err := LoadProject(root); err == nil || !strings.Contains(err.Error(), "unknown event") {
		t.Fatalf("unknown event accepted: %v", err)
	}
	if err := (Config{PreTool: {{Match: "(", Command: "x"}}}).Validate(); err == nil {
		t.Fatal("bad regexp accepted")
	}
	if err := (Config{Stop: {{Command: " "}}}).Validate(); err == nil {
		t.Fatal("empty command accepted")
	}
	m := Merge(Config{PreTool: {{Command: "a"}}}, Config{PreTool: {{Command: "b"}}, Stop: {{Command: "c"}}})
	if len(m[PreTool]) != 2 || m[PreTool][0].Command != "a" || len(m[Stop]) != 1 {
		t.Fatalf("merge = %+v", m)
	}
}
