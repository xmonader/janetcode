package agent

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
	"github.com/xmonader/janetcode/internal/verify"
)

// elideAgent is an agent with bash, the edit tools and a verify gate of
// the given commands (none: no gate), elision on unless a test says not.
func elideAgent(t *testing.T, f *fake, gate ...string) (*Agent, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX check commands")
	}
	a, root := newAgent(t, f, nil)
	a.Root = root
	a.AutoApprove = true
	a.ElideChecks = true
	a.Tools = tool.NewRegistry(tool.Bash{Root: root}, tool.WriteFile{Root: root}, tool.EditFile{Root: root}, tool.ReadFile{Root: root})
	if len(gate) > 0 {
		a.Verify = &verify.Runner{Cwd: root, Config: verify.Config{Commands: gate}}
	}
	return a, root
}

// stepResults are the tool results of the nth tool step (0-based).
func stepResults(t *testing.T, a *Agent, n int) []llm.Block {
	t.Helper()
	for _, m := range a.Messages {
		var res []llm.Block
		for _, b := range m.Blocks {
			if b.Type == llm.BlockToolResult {
				res = append(res, b)
			}
		}
		if len(res) == 0 {
			continue
		}
		if n == 0 {
			return res
		}
		n--
	}
	t.Fatal("no such tool step in the history")
	return nil
}

func lastResult(res []llm.Block) string { return res[len(res)-1].Text }

func calls(blocks ...llm.Block) *llm.Response {
	return &llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: blocks}, StopReason: llm.StopToolUse}
}

func use(id, name, in string) llm.Block {
	return llm.Block{Type: llm.BlockToolUse, ID: id, Name: name, Input: json.RawMessage(in)}
}

func bashIn(cmd string) string {
	b, _ := json.Marshal(map[string]string{"command": cmd})
	return string(b)
}

func lines(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	return strings.Count(string(data), "\n")
}

// A step that only edited gets the project's check run after it, and the
// pass rides with the step's results as one line.
func TestElideRunsTheCheckAfterAnUncheckedEdit(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"x.txt","content":"v1"}`),
		text("done"),
	}}
	a, _ := elideAgent(t, f, "test -f x.txt")
	evs, emit := collect()
	if err := a.Run(context.Background(), "go", emit); err != nil {
		t.Fatal(err)
	}
	got := lastResult(stepResults(t, a, 0))
	if !strings.Contains(got, "[harness] Checked after your edit") || !strings.Contains(got, "$ test -f x.txt — passed") {
		t.Fatalf("result = %q", got)
	}
	if strings.Count(got, "\n") > 2 {
		t.Fatalf("a pass should cost one line: %q", got)
	}
	if !strings.Contains(notices(*evs), "checked after the edit") {
		t.Fatalf("notices:\n%s", notices(*evs))
	}
}

// Off by default: an agent that was not asked for it runs nothing extra.
func TestElideIsOffByDefault(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"x.txt","content":"v1"}`),
		text("done"),
	}}
	a, root := elideAgent(t, f, "echo r >> runs.log")
	a.ElideChecks = false
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if got := lastResult(stepResults(t, a, 0)); strings.Contains(got, "[harness] Checked") {
		t.Fatalf("result = %q", got)
	}
	// The end-of-turn gate ran once; nothing else did.
	if n := lines(t, filepath.Join(root, "runs.log")); n != 1 {
		t.Fatalf("check ran %d times, want 1 (the gate)", n)
	}
}

// A failure is shown, but within the note's budget however much the
// check printed.
func TestElideShowsAFailureWithinItsBudget(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"x.txt","content":"v1"}`),
		text("done"),
		text("done"),
		text("done"),
		text("done"),
	}}
	a, _ := elideAgent(t, f, "seq 1 5000; echo FAIL: TestX; exit 1")
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	got := lastResult(stepResults(t, a, 0))
	note := got[strings.Index(got, "[harness] Checked"):]
	if !strings.Contains(note, "failed (exit 1)") || !strings.Contains(note, "FAIL: TestX") || !strings.Contains(note, "bytes omitted") {
		t.Fatalf("note = %q", note)
	}
	if len(note) > elideBudget+300 {
		t.Fatalf("note is %d bytes, over the budget of %d", len(note), elideBudget)
	}
}

// What elision leaves alone: an edit the reply already checked, an edit
// that failed, and a reply whose last change was not an edit.
func TestElideLeavesOtherStepsAlone(t *testing.T) {
	for name, step := range map[string]*llm.Response{
		"checked in the same reply": calls(
			use("1", "write_file", `{"path":"x.txt","content":"v1"}`),
			use("2", "bash", bashIn("test -f x.txt")),
		),
		"edit failed": toolCall("1", "edit_file", `{"path":"a.txt","old_string":"not there","new_string":"x"}`),
		"last change was the shell": calls(
			use("1", "write_file", `{"path":"x.txt","content":"v1"}`),
			use("2", "bash", bashIn("mkdir -p sub")),
		),
	} {
		t.Run(name, func(t *testing.T) {
			f := &fake{responses: []*llm.Response{step, text("done"), text("done"), text("done")}}
			a, root := elideAgent(t, f, "echo r >> runs.log")
			if err := a.Run(context.Background(), "go", nil); err != nil {
				t.Fatal(err)
			}
			if got := lastResult(stepResults(t, a, 0)); strings.Contains(got, "[harness] Checked") {
				t.Fatalf("result = %q", got)
			}
			if n := lines(t, filepath.Join(root, "runs.log")); n > 1 {
				t.Fatalf("check ran %d times, want at most the gate's", n)
			}
		})
	}
}

// A held-out acceptance command is never run after an edit: that would
// hand the model the hidden tests one step at a time.
func TestElideNeverRunsAHeldOutCommand(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"x.txt","content":"v1"}`),
		text("done"),
	}}
	a, root := elideAgent(t, f)
	hidden := "echo h >> hidden.log"
	a.Verify = &verify.Runner{Cwd: root, Config: verify.Config{Commands: []string{"test -f x.txt", hidden}},
		Display: map[string]string{hidden: "held-out tests"}}
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	got := lastResult(stepResults(t, a, 0))
	if !strings.Contains(got, "$ test -f x.txt — passed") || strings.Contains(got, "hidden") || strings.Contains(got, "held-out") {
		t.Fatalf("result = %q", got)
	}
	if n := lines(t, filepath.Join(root, "hidden.log")); n != 1 {
		t.Fatalf("held-out command ran %d times, want 1 (the end-of-turn gate)", n)
	}
}

// A gate made only of held-out commands gives elision nothing to run.
func TestElideWithOnlyHeldOutCommandsRunsNothing(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"x.txt","content":"v1"}`),
		text("done"),
	}}
	a, root := elideAgent(t, f)
	hidden := "echo h >> hidden.log"
	a.Verify = &verify.Runner{Cwd: root, Config: verify.Config{Commands: []string{hidden}}, Display: map[string]string{hidden: "held-out tests"}}
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if got := lastResult(stepResults(t, a, 0)); strings.Contains(got, "[harness] Checked") {
		t.Fatalf("result = %q", got)
	}
}

// The model asking for the very command the harness just ran, on a tree
// nothing changed, gets the result without a second run, marked as such.
func TestElideAnswersAnIdenticalRepeat(t *testing.T) {
	gate := "echo r >> runs.log; test -f x.txt"
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"x.txt","content":"v1"}`),
		nil, // filled below, once the root is known
		text("done"),
	}}
	a, root := elideAgent(t, f, gate)
	f.responses[1] = calls(use("t2", "read_file", `{"path":"x.txt"}`), use("t3", "bash", bashIn("cd "+root+" && "+gate+" 2>&1")))
	log := &session.Memory{}
	a.Log = log
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	res := stepResults(t, a, 1)
	if got := res[1].Text; !strings.HasPrefix(got, "[harness] Not run again") || !strings.Contains(got, "passed") {
		t.Fatalf("repeat answered with %q", got)
	}
	// Elision once and the end-of-turn gate once: the repeat never ran.
	if n := lines(t, filepath.Join(root, "runs.log")); n != 2 {
		t.Fatalf("check ran %d times, want 2", n)
	}
	var hit bool
	for _, e := range log.Events() {
		hit = hit || (e.Type == session.TypeNotice && e.Name == "elide/hit")
	}
	if !hit {
		t.Fatal("the cache hit was not logged")
	}
}

// Anything that may change the tree first, an edit in the same reply or a
// change from outside, means the repeat runs for real.
func TestElideRepeatRunsAfterTheTreeMoved(t *testing.T) {
	gate := "echo r >> runs.log; test -f x.txt"
	t.Run("an edit before it", func(t *testing.T) {
		f := &fake{responses: []*llm.Response{
			toolCall("t1", "write_file", `{"path":"x.txt","content":"v1"}`),
			calls(use("t2", "write_file", `{"path":"x.txt","content":"v2"}`), use("t3", "bash", bashIn(gate))),
			text("done"),
		}}
		a, root := elideAgent(t, f, gate)
		if err := a.Run(context.Background(), "go", nil); err != nil {
			t.Fatal(err)
		}
		if got := stepResults(t, a, 1)[1].Text; strings.Contains(got, "Not run again") {
			t.Fatalf("repeat after an edit answered from the cache: %q", got)
		}
		if n := lines(t, filepath.Join(root, "runs.log")); n != 3 {
			t.Fatalf("check ran %d times, want 3", n)
		}
	})
	t.Run("a change from outside", func(t *testing.T) {
		var root string
		touch := funcTool{name: "peek", run: func(json.RawMessage) (string, error) {
			// A read-only tool, so it is no barrier: the change is one
			// the harness can only see on disk.
			return "ok", os.WriteFile(filepath.Join(root, "x.txt"), []byte("changed from outside"), 0o644)
		}}
		f := &fake{responses: []*llm.Response{
			toolCall("t1", "write_file", `{"path":"x.txt","content":"v1"}`),
			calls(use("t2", "peek", `{}`), use("t3", "bash", bashIn(gate))),
			text("done"),
		}}
		var a *Agent
		a, root = elideAgent(t, f, gate)
		a.Tools = tool.NewRegistry(tool.Bash{Root: root}, tool.WriteFile{Root: root}, touch)
		if err := a.Run(context.Background(), "go", nil); err != nil {
			t.Fatal(err)
		}
		if got := stepResults(t, a, 1)[1].Text; strings.Contains(got, "Not run again") {
			t.Fatalf("repeat after an outside change answered from the cache: %q", got)
		}
	})
	t.Run("a different command", func(t *testing.T) {
		f := &fake{responses: []*llm.Response{
			toolCall("t1", "write_file", `{"path":"x.txt","content":"v1"}`),
			toolCall("t2", "bash", bashIn(gate+" | tail -5")),
			text("done"),
		}}
		a, _ := elideAgent(t, f, gate)
		if err := a.Run(context.Background(), "go", nil); err != nil {
			t.Fatal(err)
		}
		if got := stepResults(t, a, 1)[0].Text; strings.Contains(got, "Not run again") {
			t.Fatalf("a different command answered from the cache: %q", got)
		}
	})
}

// Without verify commands the check is the model's own last one, run as a
// bash call of its own, and only when calls are auto-approved: elision
// never puts a prompt in front of the user.
func TestElideFallsBackToTheModelsOwnCheck(t *testing.T) {
	script := func() []*llm.Response {
		return []*llm.Response{
			toolCall("t1", "bash", bashIn("test -f a.txt && echo 3 passed")),
			toolCall("t2", "write_file", `{"path":"x.txt","content":"v1"}`),
			text("done"),
		}
	}
	f := &fake{responses: script()}
	a, _ := elideAgent(t, f)
	evs, emit := collect()
	if err := a.Run(context.Background(), "go", emit); err != nil {
		t.Fatal(err)
	}
	got := lastResult(stepResults(t, a, 1))
	if !strings.Contains(got, "$ test -f a.txt && echo 3 passed — passed: 3 passed") {
		t.Fatalf("result = %q", got)
	}
	var derived bool
	for _, e := range *evs {
		derived = derived || (e.Kind == EvToolRun && e.Call != nil && strings.HasSuffix(e.Call.ID, "~elide"))
	}
	if !derived {
		t.Fatal("the fallback check did not run as a bash call of its own")
	}

	f = &fake{responses: script()}
	a, _ = elideAgent(t, f)
	a.AutoApprove = false
	ap := &namesApprover{}
	a.Approver = ap
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if got := lastResult(stepResults(t, a, 1)); strings.Contains(got, "[harness] Checked") {
		t.Fatalf("ran the model's check without auto-approve: %q", got)
	}
	for _, q := range ap.asked {
		if strings.Contains(q, "~elide") || strings.Count(strings.Join(ap.asked, "\n"), "test -f a.txt") > 1 {
			t.Fatalf("the user was asked about an elided check: %q", ap.asked)
		}
	}
}

// An inline probe is the model's own check, not the project's: it is never
// learned as the command to run after every edit.
func TestElideProjectCheck(t *testing.T) {
	for cmd, want := range map[string]bool{
		"go test ./...": true,
		"cd /w && python -m pytest -q 2>&1 | tail -5": true,
		"npx tsc --noEmit":                            true,
		"python - <<'EOF'\nassert f(1) == 2\nEOF":     false,
		`python -c "assert 1"`:                        false,
		`node -e "require('./x')"`:                    false,
		"ls -la tests":                                false,
		"":                                            false,
	} {
		if got := elideProjectCheck(cmd); got != want {
			t.Errorf("elideProjectCheck(%q) = %v, want %v", cmd, got, want)
		}
	}
}

func TestElideKey(t *testing.T) {
	root := "/w/proj"
	same := []string{"go test ./...", "cd /w/proj && go test ./...", "cd '/w/proj' && go test ./... 2>&1", " go test ./... "}
	for _, c := range same {
		if k := elideKey(c, root); k != "go test ./..." {
			t.Errorf("elideKey(%q) = %q", c, k)
		}
	}
	for _, c := range []string{"cd /w/other && go test ./...", "go test ./... | tail -5", "go test -v ./..."} {
		if k := elideKey(c, root); k == "go test ./..." {
			t.Errorf("elideKey(%q) collapsed into the plain run", c)
		}
	}
}

func TestAffectedPackages(t *testing.T) {
	pkgs := []goPkg{
		{path: "m/a", dir: "/m/a"},
		{path: "m/b", dir: "/m/b", deps: []string{"fmt", "m/a"}},
		{path: "m/c", dir: "/m/c", tests: []string{"m/t"}},
		{path: "m/t", dir: "/m/t", deps: []string{"m/a"}},
		{path: "m/d", dir: "/m/d", deps: []string{"fmt"}},
	}
	got := strings.Join(affectedPackages(pkgs, []string{"/m/a/x.go"}), " ")
	if got != "m/a m/b m/c m/t" {
		t.Fatalf("affected = %q", got)
	}
	if got := affectedPackages(pkgs, []string{"/m/d/x_test.go"}); strings.Join(got, " ") != "m/d" {
		t.Fatalf("affected = %q", got)
	}
}

// elideScope narrows `go test ./...` in a real module, keeps the rest of
// the command, and leaves alone what it cannot narrow usefully.
func TestElideScopeNarrowsGoTest(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go toolchain")
	}
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/m\n\ngo 1.21\n")
	write("a/a.go", "package a\n\nfunc A() int { return 1 }\n")
	write("b/b.go", "package b\n\nimport \"example.com/m/a\"\n\nfunc B() int { return a.A() }\n")
	for _, p := range []string{"c", "d", "e"} {
		write(p+"/"+p+".go", "package "+p+"\n")
	}
	edited := []string{filepath.Join(root, "a", "a.go")}
	got := elideScope(context.Background(), root, "cd "+root+" && go test -count=1 ./... 2>&1 | tail -5", edited)
	want := "cd " + root + " && go test -count=1 example.com/m/a example.com/m/b 2>&1 | tail -5"
	if got != want {
		t.Fatalf("scoped = %q\nwant     %q", got, want)
	}
	for name, c := range map[string]struct {
		cmd    string
		edited []string
	}{
		"not go test":     {"go vet ./...", edited},
		"not a Go edit":   {"go test ./...", []string{filepath.Join(root, "README.md")}},
		"most of the mod": {"go test ./...", []string{filepath.Join(root, "a", "a.go"), filepath.Join(root, "c", "c.go"), filepath.Join(root, "d", "d.go")}},
	} {
		if got := elideScope(context.Background(), root, c.cmd, c.edited); got != "" {
			t.Errorf("%s: scoped to %q, want left alone", name, got)
		}
	}
}

// The pre-screen counts, from a recorded run, what elision would have done.
func TestScreenElision(t *testing.T) {
	var evs []session.Event
	seq := 0
	reply := func(turn int, cs ...[3]string) {
		evs = append(evs, session.Event{Type: session.TypeAssistant, Turn: turn})
		for _, c := range cs {
			seq++
			id := "c" + string(rune('a'+seq))
			evs = append(evs,
				session.Event{Type: session.TypeToolCall, CallID: id, Name: c[0], Arguments: c[1]},
				session.Event{Type: session.TypeToolResult, CallID: id, Name: c[0], Output: c[2], IsError: c[2] == "ERR"})
		}
	}
	edit := [3]string{"edit_file", `{"path":"a.py"}`, "Edited a.py"}
	pytest := [3]string{"bash", bashIn("python -m pytest -q"), "3 passed"}
	probe := [3]string{"bash", bashIn("python - <<'EOF'\nassert 1\nEOF"), ""}
	read := [3]string{"read_file", `{"path":"a.py"}`, "x"}
	reply(1, pytest)                                           // the model's own check, learned
	reply(1, edit)                                             // trigger: next is the same check alone
	reply(1, read, pytest)                                     // hit (reads do not count against it)
	reply(1, edit, pytest)                                     // batched, not a trigger
	reply(1, edit)                                             // trigger: next edits first
	reply(1, edit)                                             // trigger: next is a probe
	reply(1, probe)                                            //
	reply(1, [3]string{"edit_file", `{"path":"a.py"}`, "ERR"}) // a failed edit is not a trigger
	reply(1, edit)                                             // trigger: the turn ends
	reply(1)                                                   // text only
	reply(2, edit)                                             // trigger: next reply is another turn
	reply(3, pytest, edit)                                     // a trigger at the end of the log
	s := ScreenElision(evs)
	if s.Triggers != 6 || s.HitAlone != 0 || s.HitMixed != 1 || s.Batched != 1 || s.MissEdit != 1 || s.MissOther != 1 || s.MissEnd != 3 {
		t.Fatalf("screen = %+v", s)
	}
	if s.SameCommand != 1 || s.HitBytes != len("3 passed") {
		t.Fatalf("screen = %+v", s)
	}
}
