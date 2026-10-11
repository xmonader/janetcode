package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/tool"
)

const occGo = `package pkg

// A says a.
func A() string {
	return "a"
}

// B says b.
func B() string {
	return "b"
}
`

// occScript drives an editing subagent step by step. step gets the
// number of tool results in the current run (the first prompt or the
// repair brief) and whether this run is the repair round, and returns
// the next reply. briefs collects every repair brief the harness sent.
type occScript struct {
	mu     sync.Mutex
	step   func(n int, repair bool) *llm.Response
	briefs []string
}

func (s *occScript) Name() string { return "fake" }
func (s *occScript) Stream(ctx context.Context, req llm.Request, emit func(llm.Event)) (*llm.Response, error) {
	n, repair := 0, false
	for i := len(req.Messages) - 1; i >= 0; i-- {
		m := req.Messages[i]
		if m.Role == llm.RoleUser && strings.TrimSpace(m.Text()) != "" && m.Blocks[0].Type != llm.BlockToolResult {
			if strings.Contains(m.Text(), "While you worked") {
				repair = true
				s.mu.Lock()
				if len(s.briefs) == 0 || s.briefs[len(s.briefs)-1] != m.Text() {
					s.briefs = append(s.briefs, m.Text())
				}
				s.mu.Unlock()
			}
			break
		}
		for _, b := range m.Blocks {
			if b.Type == llm.BlockToolResult {
				n++
			}
		}
	}
	return s.step(n, repair), nil
}

func occSay(text string) *llm.Response {
	return &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, text), StopReason: llm.StopEndTurn}
}

func editCall(id, path, old, new string) *llm.Response {
	in, _ := json.Marshal(map[string]string{"path": path, "old_string": old, "new_string": new})
	return toolCall(id, "edit_file", string(in))
}

// occSetup is a committed repository holding files, and a parent with
// merge-back on whose task tool runs prov.
func occSetup(t *testing.T, files map[string]string, prov llm.Provider) (string, *Agent, *Task) {
	t.Helper()
	root := t.TempDir()
	for p, c := range files {
		os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755)
		os.WriteFile(filepath.Join(root, p), []byte(c), 0o644)
	}
	gitInit(t, root)
	parent := &Agent{Provider: prov, Model: "m", System: "sys", AutoApprove: true, SubagentMerge: true}
	task := &Task{Parent: parent, Root: root}
	parent.Tools = tool.NewRegistry(task)
	t.Cleanup(func() { task.Close(context.Background()) })
	return root, parent, task
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Two changes to different functions of one file commute: the subagent's
// edit lands in the project next to the one made while it worked, with
// no model call spent on the overlap and a provenance record for the
// merge.
func TestMergeBackCommutingEditsApply(t *testing.T) {
	var root string
	prov := &occScript{}
	prov.step = func(n int, repair bool) *llm.Response {
		switch n {
		case 0:
			return editCall("c1", "a.go", `return "a"`, `return "A"`)
		default:
			// Someone else changes B in the project meanwhile.
			p := filepath.Join(root, "a.go")
			os.WriteFile(p, []byte(strings.Replace(readFile(t, p), `return "b"`, `return "B"`, 1)), 0o644)
			return occSay("REPORT: A now says A")
		}
	}
	root, parent, task := occSetup(t, map[string]string{"a.go": occGo}, prov)
	out, err := task.Run(context.Background(), json.RawMessage(`{"description":"change A","prompt":"change A","edit":true}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"REPORT: A now says A", "## Merged into the project by the harness", "merged with changes made meanwhile", "a.go", `+	return "A"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if got := readFile(t, filepath.Join(root, "a.go")); !strings.Contains(got, `return "A"`) || !strings.Contains(got, `return "B"`) {
		t.Fatalf("project a.go lost a side:\n%s", got)
	}
	if len(prov.briefs) != 0 {
		t.Fatalf("a commuting merge sent a repair brief: %q", prov.briefs)
	}
	parent.noteSubagentMerges()
	if !parent.mutated {
		t.Fatal("a merge did not make the turn a mutating one, so the verify gate would not run")
	}
	ch := parent.Changes()
	if len(ch) != 1 || ch[0].Tool != "task" || !strings.Contains(ch[0].Intent, "change A") {
		t.Fatalf("changes = %+v", ch)
	}
}

// A continued subagent is measured from where the last merge left its
// checkout: what that merge brought in is not handed back as new work,
// and a further edit merges on its own.
func TestMergeBackContinuedSubagent(t *testing.T) {
	var root string
	round := 0
	prov := &occScript{}
	prov.step = func(n int, repair bool) *llm.Response {
		switch {
		case round == 0 && n == 0:
			return editCall("c1", "a.go", `return "a"`, `return "A"`)
		case round == 0:
			p := filepath.Join(root, "a.go")
			os.WriteFile(p, []byte(strings.Replace(readFile(t, p), `return "b"`, `return "B"`, 1)), 0o644)
			return occSay("REPORT: first")
		case round == 1:
			return occSay("REPORT: nothing more to do")
		case n == 0:
			return editCall("c3", "a.go", `// A says a.`, `// A says A.`)
		default:
			return occSay("REPORT: doc fixed")
		}
	}
	root, _, task := occSetup(t, map[string]string{"a.go": occGo}, prov)
	if _, err := task.Run(context.Background(), json.RawMessage(`{"description":"change A","prompt":"change A","edit":true}`)); err != nil {
		t.Fatal(err)
	}
	round = 1
	out, err := task.Run(context.Background(), json.RawMessage(`{"continue":"task-1","prompt":"anything else?"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "changed nothing that is not already in the project") || strings.Contains(out, "+\treturn \"B\"") {
		t.Fatalf("continued run re-reported merged work:\n%s", out)
	}
	round = 2
	out, err = task.Run(context.Background(), json.RawMessage(`{"continue":"task-1","prompt":"fix the doc"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "applied as the subagent left them: a.go") {
		t.Fatalf("second merge:\n%s", out)
	}
	got := readFile(t, filepath.Join(root, "a.go"))
	for _, want := range []string{"// A says A.", `return "A"`, `return "B"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("project a.go missing %s:\n%s", want, got)
		}
	}
}

// Two changes to the same function do not commute. The subagent is sent
// back once with both deltas, in a checkout that now holds the other
// version, and what it re-applies there is merged.
func TestMergeBackOverlapGoesBackForOneRepair(t *testing.T) {
	var root string
	prov := &occScript{}
	prov.step = func(n int, repair bool) *llm.Response {
		if repair {
			if n == 0 {
				// The checkout holds the other side's version now.
				return editCall("r1", "a.go", `return "a" + "!"`, `return "A" + "!"`)
			}
			return occSay("REPORT: re-applied on top of the other change")
		}
		switch n {
		case 0:
			return editCall("c1", "a.go", `return "a"`, `return "A"`)
		default:
			p := filepath.Join(root, "a.go")
			os.WriteFile(p, []byte(strings.Replace(readFile(t, p), `return "a"`, `return "a" + "!"`, 1)), 0o644)
			return occSay("REPORT: A now says A")
		}
	}
	root, _, task := occSetup(t, map[string]string{"a.go": occGo}, prov)
	out, err := task.Run(context.Background(), json.RawMessage(`{"description":"change A","prompt":"change A","edit":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(prov.briefs) != 1 {
		t.Fatalf("repair briefs = %d, want 1", len(prov.briefs))
	}
	brief := prov.briefs[0]
	for _, want := range []string{"a.go (overlap)", "both sides changed A", "Your change:", `+	return "A"`, "The other change:", `+	return "a" + "!"`, "only repair round"} {
		if !strings.Contains(brief, want) {
			t.Fatalf("brief missing %q:\n%s", want, brief)
		}
	}
	if !strings.Contains(out, "1 conflict sent back to the subagent and resolved") || !strings.Contains(out, "After the merge repair round:") {
		t.Fatalf("report:\n%s", out)
	}
	if got := readFile(t, filepath.Join(root, "a.go")); !strings.Contains(got, `return "A" + "!"`) {
		t.Fatalf("project a.go:\n%s", got)
	}
}

// A conflict the repair round does not fix is returned, not forced: the
// project keeps the other side's version and the subagent's work comes
// back as a patch against it.
func TestMergeBackUnresolvedConflictLeavesProjectAlone(t *testing.T) {
	var root string
	prov := &occScript{}
	prov.step = func(n int, repair bool) *llm.Response {
		if repair {
			if n == 0 {
				// Ignores the brief and redoes its own version, clobbering
				// the other change's line once more.
				return editCall("r1", "a.go", `return "a" + "!"`, `return "A"`)
			}
			if n == 1 {
				p := filepath.Join(root, "a.go")
				os.WriteFile(p, []byte(strings.Replace(readFile(t, p), `return "a" + "!"`, `return "a" + "?"`, 1)), 0o644)
			}
			return occSay("REPORT: done")
		}
		switch n {
		case 0:
			return editCall("c1", "a.go", `return "a"`, `return "A"`)
		default:
			p := filepath.Join(root, "a.go")
			os.WriteFile(p, []byte(strings.Replace(readFile(t, p), `return "a"`, `return "a" + "!"`, 1)), 0o644)
			return occSay("REPORT: A now says A")
		}
	}
	root, parent, task := occSetup(t, map[string]string{"a.go": occGo}, prov)
	out, err := task.Run(context.Background(), json.RawMessage(`{"description":"change A","prompt":"change A","edit":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "## Not merged: conflicts remain after one repair round") || !strings.Contains(out, "git apply ") {
		t.Fatalf("report:\n%s", out)
	}
	if got := readFile(t, filepath.Join(root, "a.go")); !strings.Contains(got, `return "a" + "?"`) || strings.Contains(got, `return "A"`) {
		t.Fatalf("project a.go was changed by an unmerged subagent:\n%s", got)
	}
	if len(prov.briefs) != 1 {
		t.Fatalf("repair rounds = %d, want exactly 1", len(prov.briefs))
	}
	parent.noteSubagentMerges()
	if parent.mutated {
		t.Fatal("an unmerged subagent marked the turn as mutating")
	}
}

const occAPI = `package pkg

// Limit is the cap.
func Limit() int { return 10 }

// Name is unrelated.
func Name() string { return "n" }
`

// A file the subagent read and did not edit is part of its read-set. A
// change to a declaration its edits call sends it back to re-judge; a
// change elsewhere in the same file does not.
func TestMergeBackStaleReadIsRejudged(t *testing.T) {
	for _, tc := range []struct {
		name, old, new string
		wantBrief      bool
	}{
		{"used", "return 10", "return 20", true},
		{"unused", `return "n"`, `return "m"`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var root string
			prov := &occScript{}
			prov.step = func(n int, repair bool) *llm.Response {
				if repair {
					return occSay("REPORT: checked, the call still fits")
				}
				switch n {
				case 0:
					return toolCall("c1", "read_file", `{"path":"api.go"}`)
				case 1:
					return editCall("c2", "use.go", "return 1", "return Limit()")
				default:
					p := filepath.Join(root, "api.go")
					os.WriteFile(p, []byte(strings.Replace(readFile(t, p), tc.old, tc.new, 1)), 0o644)
					return occSay("REPORT: Use calls Limit")
				}
			}
			root, _, task := occSetup(t, map[string]string{"api.go": occAPI, "use.go": "package pkg\n\nfunc Use() int { return 1 }\n"}, prov)
			out, err := task.Run(context.Background(), json.RawMessage(`{"description":"use limit","prompt":"use limit","edit":true}`))
			if err != nil {
				t.Fatal(err)
			}
			if got := len(prov.briefs) > 0; got != tc.wantBrief {
				t.Fatalf("repair brief sent = %v, want %v; briefs=%q", got, tc.wantBrief, prov.briefs)
			}
			if tc.wantBrief && !strings.Contains(prov.briefs[0], "api.go (stale-read)") {
				t.Fatalf("brief:\n%s", prov.briefs[0])
			}
			if !tc.wantBrief && !strings.Contains(out, "in nothing its edits use: api.go") {
				t.Fatalf("independent read not reported:\n%s", out)
			}
			if got := readFile(t, filepath.Join(root, "use.go")); !strings.Contains(got, "return Limit()") {
				t.Fatalf("use.go not merged:\n%s", got)
			}
		})
	}
}

// Merge-back never asks: a session whose edits need approval gets the
// patch back the old way, and the project is untouched.
func TestMergeBackNeedsEditsPreApproved(t *testing.T) {
	prov := &occScript{}
	prov.step = func(n int, repair bool) *llm.Response {
		if n == 0 {
			return editCall("c1", "a.go", `return "a"`, `return "A"`)
		}
		return occSay("REPORT: A now says A")
	}
	root, parent, task := occSetup(t, map[string]string{"a.go": occGo}, prov)
	parent.AutoApprove = false
	out, err := task.Run(context.Background(), json.RawMessage(`{"description":"change A","prompt":"change A","edit":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "merge-back not done: writing a.go would need your approval") || !strings.Contains(out, "## Changes made in the worktree (not applied)") {
		t.Fatalf("report:\n%s", out)
	}
	if got := readFile(t, filepath.Join(root, "a.go")); got != occGo {
		t.Fatalf("project changed without approval:\n%s", got)
	}
}

// The greps a subagent runs are range reads: one whose answer a
// concurrent change altered is sent back even though no file it read
// by name changed.
func TestMergeBackStaleGrep(t *testing.T) {
	var root string
	prov := &occScript{}
	prov.step = func(n int, repair bool) *llm.Response {
		if repair {
			return occSay("REPORT: handled the new handler too")
		}
		switch n {
		case 0:
			return toolCall("c1", "grep", `{"pattern":"func Handler","path":"pkg"}`)
		case 1:
			return editCall("c2", "pkg/h1.go", "return 1", "return 2")
		default:
			os.WriteFile(filepath.Join(root, "pkg", "h2.go"), []byte("package pkg\n\nfunc Handler2() int { return 1 }\n"), 0o644)
			return occSay("REPORT: changed every handler")
		}
	}
	root, _, task := occSetup(t, map[string]string{"pkg/h1.go": "package pkg\n\nfunc Handler1() int { return 1 }\n"}, prov)
	if _, err := task.Run(context.Background(), json.RawMessage(`{"description":"handlers","prompt":"handlers","edit":true}`)); err != nil {
		t.Fatal(err)
	}
	if len(prov.briefs) != 1 || !strings.Contains(prov.briefs[0], "stale-pattern") || !strings.Contains(prov.briefs[0], "pkg/h2.go") {
		t.Fatalf("briefs = %q", prov.briefs)
	}
}

// fanScript runs several subagents at once, each told by its prompt which
// function to change. Each waits at the barrier after its edit, so every
// one of them works from the same base and finishes into a project the
// others are changing.
type fanScript struct{ barrier *sync.WaitGroup }

func (s *fanScript) Name() string { return "fake" }
func (s *fanScript) Stream(ctx context.Context, req llm.Request, emit func(llm.Event)) (*llm.Response, error) {
	fn := strings.TrimPrefix(req.Messages[0].Text(), "change ")
	n := 0
	for _, m := range req.Messages {
		for _, b := range m.Blocks {
			if b.Type == llm.BlockToolResult {
				n++
			}
		}
	}
	if n == 0 {
		lower := strings.ToLower(fn)
		return editCall("c-"+fn, "a.go", `return "`+lower+`"`, `return "`+fn+`"`), nil
	}
	if n == 1 {
		s.barrier.Done()
		s.barrier.Wait()
	}
	return occSay("REPORT: changed " + fn), nil
}

// Three subagents editing three functions of one hotspot file all land,
// with no conflict reaching any model: the overlap the fanout set is
// about, settled by the commutation check alone.
func TestMergeBackParallelSubagentsOnOneFile(t *testing.T) {
	src := occGo + "\n// C says c.\nfunc C() string {\n\treturn \"c\"\n}\n"
	var barrier sync.WaitGroup
	barrier.Add(3)
	root, _, task := occSetup(t, map[string]string{"a.go": src}, &fanScript{barrier: &barrier})
	var wg sync.WaitGroup
	outs := make([]string, 3)
	for i, fn := range []string{"A", "B", "C"} {
		wg.Add(1)
		go func(i int, fn string) {
			defer wg.Done()
			out, err := task.Run(context.Background(), json.RawMessage(`{"description":"change `+fn+`","prompt":"change `+fn+`","edit":true}`))
			if err != nil {
				t.Error(err)
			}
			outs[i] = out
		}(i, fn)
	}
	wg.Wait()
	got := readFile(t, filepath.Join(root, "a.go"))
	for _, want := range []string{`return "A"`, `return "B"`, `return "C"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("project a.go missing %s:\n%s\nreports:\n%s", want, got, strings.Join(outs, "\n---\n"))
		}
	}
	merged := 0
	for _, o := range outs {
		if strings.Contains(o, "repair round") {
			t.Fatalf("a commuting fan-out spent a repair round:\n%s", o)
		}
		if strings.Contains(o, "merged with changes made meanwhile") {
			merged++
		}
	}
	if merged != 2 {
		t.Fatalf("%d of 3 merged with concurrent changes, want 2 (the first lands clean)", merged)
	}
}
