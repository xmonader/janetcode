// Package action holds the GitHub Action's script (run.sh, called by
// action.yml at the repository root) and the tests that keep the two and
// the example workflows in step.
package action

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// declaredInputs reads the input names under action.yml's inputs: block.
func declaredInputs(t *testing.T) map[string]bool {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "action.yml"))
	if err != nil {
		t.Fatal(err)
	}
	in := map[string]bool{}
	section := ""
	key := regexp.MustCompile(`^  ([a-z][a-z-]*):`)
	for _, line := range strings.Split(string(b), "\n") {
		if len(line) > 0 && line[0] != ' ' {
			section = strings.TrimSuffix(strings.TrimSpace(line), ":")
			continue
		}
		if m := key.FindStringSubmatch(line); m != nil && section == "inputs" {
			in[m[1]] = true
		}
	}
	return in
}

// Every inputs.X action.yml uses is declared, and every with: key the
// example workflows pass is an input the action has.
func TestActionInputsAreDeclared(t *testing.T) {
	in := declaredInputs(t)
	if len(in) < 10 {
		t.Fatalf("read only %d inputs from action.yml", len(in))
	}
	b, _ := os.ReadFile(filepath.Join("..", "..", "action.yml"))
	for _, m := range regexp.MustCompile(`inputs\.([a-z][a-z-]*)`).FindAllStringSubmatch(string(b), -1) {
		if !in[m[1]] {
			t.Errorf("action.yml uses inputs.%s, which it does not declare", m[1])
		}
	}
	if !strings.Contains(string(b), `"$GITHUB_ACTION_PATH/ci/action/run.sh"`) {
		t.Error("action.yml does not run ci/action/run.sh")
	}
	examples, _ := filepath.Glob(filepath.Join("..", "..", "docs", "examples", "github", "*.yml"))
	if len(examples) == 0 {
		t.Fatal("no example workflows")
	}
	for _, f := range examples {
		b, _ := os.ReadFile(f)
		inWith := false
		for _, line := range strings.Split(string(b), "\n") {
			trim := strings.TrimSpace(line)
			if trim == "with:" {
				inWith = true
				continue
			}
			if inWith {
				k, _, ok := strings.Cut(trim, ":")
				if !ok || strings.HasPrefix(trim, "-") || !strings.HasPrefix(line, "          ") {
					inWith = false
					continue
				}
				if !in[k] {
					t.Errorf("%s passes %s, which action.yml does not declare", filepath.Base(f), k)
				}
			}
		}
	}
}

// fakeTools puts a janetcode and a gh on PATH that record what they were
// asked and answer as the real ones would.
func fakeTools(t *testing.T) (dir string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("bash script")
	}
	for _, tool := range []string{"bash", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip("needs " + tool)
		}
	}
	dir = t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write("janetcode", `#!/bin/sh
printf '%s\n' "$@" > "`+dir+`/janetcode.args"
echo '{"type":"result","result":"Looks fine except a.go:3.","exit_code":0,"session_id":"s1","model":"m1","cost_usd":0.012}'
`)
	write("gh", `#!/bin/sh
echo "$@" >> "`+dir+`/gh.log"
case "$1 $2" in
"pr diff") printf 'diff --git a/a.go b/a.go\n+bad()\n' ;;
"api --method") for a; do case "$a" in body=@*) cat "${a#body=@}" > "`+dir+`/comment.md";; esac; done ;;
esac
`)
	return dir
}

func runAction(t *testing.T, tools string, event string, env ...string) (string, error) {
	t.Helper()
	tmp := t.TempDir()
	ev := filepath.Join(tmp, "event.json")
	os.WriteFile(ev, []byte(event), 0o644)
	out := filepath.Join(tmp, "out")
	cmd := exec.Command("bash", "run.sh")
	cmd.Env = append(os.Environ(),
		"PATH="+tools+string(os.PathListSeparator)+os.Getenv("PATH"),
		"JANETCODE_BIN="+filepath.Join(tools, "janetcode"),
		"GITHUB_EVENT_PATH="+ev, "GITHUB_REPOSITORY=acme/tool", "GITHUB_OUTPUT="+out, "RUNNER_TEMP="+tmp,
		"INPUT_MAX_COST=1.00", "INPUT_COMMENT=true", "INPUT_FAIL_ON_ERROR=true",
	)
	cmd.Env = append(cmd.Env, env...)
	b, err := cmd.CombinedOutput()
	o, _ := os.ReadFile(out)
	if err != nil {
		t.Logf("run.sh output:\n%s", b)
	}
	return string(o), err
}

func TestReviewModeCommentsTheAnswer(t *testing.T) {
	tools := fakeTools(t)
	out, err := runAction(t, tools, `{"pull_request":{"number":5,"title":"Add bad","body":"desc"}}`, "GITHUB_EVENT_NAME=pull_request", "INPUT_MODE=auto")
	if err != nil {
		t.Fatal(err)
	}
	args, _ := os.ReadFile(filepath.Join(tools, "janetcode.args"))
	for _, want := range []string{"--readonly", "--output-format\njson", "--max-cost\n1.00", "+bad()", "pull request #5 (Add bad)"} {
		if !strings.Contains(string(args), want) {
			t.Errorf("janetcode args lack %q:\n%s", want, args)
		}
	}
	comment, _ := os.ReadFile(filepath.Join(tools, "comment.md"))
	if !strings.Contains(string(comment), "Looks fine except a.go:3.") || !strings.Contains(string(comment), "$0.0120") {
		t.Errorf("comment = %s", comment)
	}
	log, _ := os.ReadFile(filepath.Join(tools, "gh.log"))
	if !strings.Contains(string(log), "repos/acme/tool/issues/5/comments") {
		t.Errorf("gh log = %s", log)
	}
	if !strings.Contains(out, "exit-code<<") || !strings.Contains(out, "Looks fine") || !strings.Contains(out, "\nfalse\n") {
		t.Errorf("outputs = %s", out)
	}
}

func TestMentionModeIgnoresStrangersAndMissingTrigger(t *testing.T) {
	tools := fakeTools(t)
	out, err := runAction(t, tools, `{"issue":{"number":3,"title":"t"},"comment":{"body":"@janetcode fix it","author_association":"NONE"}}`, "GITHUB_EVENT_NAME=issue_comment")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "skipped<<") || !strings.Contains(out, "\ntrue\n") {
		t.Fatalf("a stranger's mention was not skipped: %s", out)
	}
	if _, err := os.Stat(filepath.Join(tools, "janetcode.args")); err == nil {
		t.Fatal("janetcode ran for a stranger")
	}
	out, _ = runAction(t, tools, `{"issue":{"number":3},"comment":{"body":"thanks!","author_association":"OWNER"}}`, "GITHUB_EVENT_NAME=issue_comment")
	if !strings.Contains(out, "\ntrue\n") {
		t.Fatalf("a comment without the trigger was not skipped: %s", out)
	}
}

func TestMentionModeAnswersAMember(t *testing.T) {
	tools := fakeTools(t)
	_, err := runAction(t, tools, `{"issue":{"number":3,"title":"Crash","body":"it crashes","pull_request":{"url":"x"}},"comment":{"body":"@janetcode why?","author_association":"MEMBER"}}`, "GITHUB_EVENT_NAME=issue_comment")
	if err != nil {
		t.Fatal(err)
	}
	args, _ := os.ReadFile(filepath.Join(tools, "janetcode.args"))
	if !strings.Contains(string(args), "pull request #3 (Crash)") || !strings.Contains(string(args), "@janetcode why?") || !strings.Contains(string(args), "--readonly") {
		t.Fatalf("args = %s", args)
	}
}
