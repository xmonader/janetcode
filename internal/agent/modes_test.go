package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

// TestModeGateSameForModelAndScriptCalls drives one mutating call through
// executeTool (a call the model made) and runNested (the same call made
// from a script or recipe) in every mode that refuses it, and requires the
// two paths to refuse identically: the same message, flagged as a denial,
// emitted to the front-end and written to the session log. The two used
// to carry separate copies of the gate, and the nested one had drifted.
func TestModeGateSameForModelAndScriptCalls(t *testing.T) {
	cases := []struct {
		name  string
		setup func(a *Agent, root, lockedTest string)
		input func(root, lockedTest string) string
	}{
		{"read-only", func(a *Agent, _, _ string) { a.ReadOnly = true }, implInput},
		{"plan", func(a *Agent, _, _ string) { a.PlanMode = true }, implInput},
		{"accept writing", func(a *Agent, root, _ string) {
			a.SetAccept(&session.AcceptMeta{Phase: session.AcceptWriting, Request: "add foo", Root: root}, nil)
		}, implInput},
		{"accept proposed", func(a *Agent, root, _ string) {
			a.SetAccept(&session.AcceptMeta{Phase: session.AcceptProposed, Request: "add foo", Root: root, Proposed: "go test ./..."}, nil)
		}, implInput},
		{"accept locked", func(a *Agent, root, lockedTest string) {
			sum, _ := hashFile(lockedTest)
			a.SetAccept(&session.AcceptMeta{Phase: session.AcceptLocked, Request: "add foo", Root: root,
				Files: []string{lockedTest}, Hashes: map[string]string{lockedTest: sum}, Command: "true"}, nil)
		}, func(_, _ string) string { return `{"path":"foo_test.go","content":"package foo // changed\n"}` }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			type outcome struct {
				msg    string
				isErr  bool
				events []Event
				logged []session.Event
			}
			run := func(nested bool) outcome {
				a, root := newAgent(t, &fake{}, ApproverFunc(func(context.Context, ToolCall) (Decision, error) { return Allow, nil }))
				a.AutoApprove = true
				a.Tools = tool.NewRegistry(tool.WriteFile{Root: root})
				mem := &session.Memory{}
				a.Log = mem
				lockedTest := filepath.Join(root, "foo_test.go")
				if err := os.WriteFile(lockedTest, []byte("package foo\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				c.setup(a, root, lockedTest)
				evs, emit := collect()
				input := c.input(root, lockedTest)
				var o outcome
				if nested {
					a.emitNow = emit
					var denied bool
					o.msg, o.isErr, denied = a.runNested(context.Background(), "c1", "write_file", []byte(input))
					if !denied {
						t.Errorf("runNested did not report the refusal as a denial")
					}
				} else {
					var err error
					o.msg, o.isErr, err = a.executeTool(context.Background(), llm.Block{ID: "c1", Name: "write_file", Input: []byte(input)}, emit)
					if err != nil {
						t.Fatal(err)
					}
				}
				o.events = *evs
				for _, e := range mem.Ev {
					if e.Type == session.TypeToolResult {
						o.logged = append(o.logged, e)
					}
				}
				return o
			}
			model, script := run(false), run(true)
			if model.msg == "" || !model.isErr || !script.isErr {
				t.Fatalf("call was not refused: model=%+v script=%+v", model, script)
			}
			if model.msg != script.msg {
				t.Fatalf("refusals differ:\nmodel:  %s\nscript: %s", model.msg, script.msg)
			}
			for who, o := range map[string]outcome{"model": model, "script": script} {
				if len(o.events) != 1 || o.events[0].Kind != EvToolResult || !o.events[0].Denied || o.events[0].Output != o.msg {
					t.Errorf("%s path events = %+v", who, o.events)
				}
				if len(o.logged) != 1 || o.logged[0].Output != o.msg || !o.logged[0].IsError {
					t.Errorf("%s path logged = %+v", who, o.logged)
				}
			}
		})
	}
}

func implInput(_, _ string) string { return `{"path":"impl.go","content":"package foo\n"}` }
