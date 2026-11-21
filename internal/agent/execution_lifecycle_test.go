package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/hook"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

// Like job, this tool's mutation status depends on its arguments.
type actionTool struct{ runs *int }

func (actionTool) Name() string            { return "action" }
func (actionTool) Description() string     { return "read or write" }
func (actionTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (actionTool) Mutating() bool          { return false }
func (actionTool) MutatesCall(in json.RawMessage) bool {
	return strings.Contains(string(in), `"write"`)
}
func (t actionTool) Run(context.Context, json.RawMessage) (string, error) {
	(*t.runs)++
	return "ran", nil
}

func TestHookCannotPromoteReadToMutation(t *testing.T) {
	for _, nested := range []bool{false, true} {
		for _, readOnly := range []bool{false, true} {
			t.Run(fmtLifecycle(nested, readOnly), func(t *testing.T) {
				runs := 0
				a, root := newAgent(t, &fake{}, nil)
				a.Tools = tool.NewRegistry(actionTool{&runs})
				a.AutoApprove, a.ReadOnly = true, readOnly
				a.Hooks = &hook.Runner{Cwd: root, Config: hook.Config{Hooks: hook.Hooks{
					hook.PreTool: {{Match: "action", Command: `echo '{"input":{"action":"write"}}'`}},
				}}}
				u := llm.Block{ID: "a", Name: "action", Input: json.RawMessage(`{"action":"read"}`)}
				var out string
				var failed bool
				if nested {
					out, failed, _ = a.runNested(context.Background(), u.ID, u.Name, u.Input)
				} else {
					out, _, failed, _ = a.execute(context.Background(), u, func(Event) {})
				}
				if runs != 0 || !failed || !strings.Contains(out, "hook") {
					t.Fatalf("rewritten call escaped: runs=%d failed=%v out=%q", runs, failed, out)
				}
			})
		}
	}
}

func fmtLifecycle(nested, flag bool) string {
	path := "direct"
	if nested {
		path = "nested"
	}
	if flag {
		path += "/on"
	} else {
		path += "/off"
	}
	return path
}

func TestNestedEditsTrackAcceptanceState(t *testing.T) {
	for _, phase := range []session.AcceptPhase{session.AcceptWriting, session.AcceptProposed, session.AcceptUnlocked} {
		t.Run(string(phase), func(t *testing.T) {
			a, root := newAgent(t, &fake{}, nil)
			a.AutoApprove = true
			path := filepath.Join(root, "foo_test.go")
			if err := os.WriteFile(path, []byte("package foo\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			sum, err := hashFile(path)
			if err != nil {
				t.Fatal(err)
			}
			meta := &session.AcceptMeta{Phase: phase, Root: root}
			if phase != session.AcceptWriting {
				meta.Files, meta.Hashes = []string{path}, map[string]string{path: sum}
			}
			a.SetAccept(meta, nil)
			out, failed, denied := a.runNested(context.Background(), "w", "write_file", json.RawMessage(`{"path":"foo_test.go","content":"package foo // revised\n"}`))
			if failed || denied {
				t.Fatalf("write failed: %s", out)
			}
			got := a.AcceptMeta()
			if !slices.Contains(got.Files, path) {
				t.Fatal("nested acceptance file was not registered")
			}
			if phase == session.AcceptProposed && got.Phase != session.AcceptWriting {
				t.Fatal("nested revision did not invalidate the proposed command")
			}
			if phase == session.AcceptUnlocked {
				want, err := hashFile(path)
				if err != nil || got.Hashes[path] != want {
					t.Fatalf("nested edit left stale hash: %s, want %s (%v)", got.Hashes[path], want, err)
				}
			}
		})
	}
}

func TestFailedMutatorsRestoreLocksAndRecordEffects(t *testing.T) {
	for _, nested := range []bool{false, true} {
		for _, failure := range []string{"error", "panic", "cancel"} {
			t.Run(fmtLifecycle(nested, false)+"/"+failure, func(t *testing.T) {
				a, root := newAgent(t, &fake{}, nil)
				a.AutoApprove = true
				path := filepath.Join(root, "foo_test.go")
				original := []byte("package foo\n")
				if err := os.WriteFile(path, original, 0o644); err != nil {
					t.Fatal(err)
				}
				sum, err := hashFile(path)
				if err != nil {
					t.Fatal(err)
				}
				a.SetAccept(&session.AcceptMeta{Phase: session.AcceptLocked, Root: root, Files: []string{path}, Hashes: map[string]string{path: sum}, Contents: map[string][]byte{path: original}}, nil)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				a.Tools = tool.NewRegistry(funcTool{name: "partial", mutating: true, run: func(json.RawMessage) (string, error) {
					if err := os.WriteFile(path, []byte("tampered"), 0o644); err != nil {
						return "", err
					}
					if failure == "panic" {
						panic("after writing")
					}
					if failure == "cancel" {
						cancel()
						return "", ctx.Err()
					}
					return "", errors.New("after writing")
				}})
				if nested {
					a.runNested(ctx, "p", "partial", json.RawMessage(`{}`))
				} else {
					a.execute(ctx, llm.Block{ID: "p", Name: "partial", Input: json.RawMessage(`{}`)}, func(Event) {})
				}
				got, err := os.ReadFile(path)
				if err != nil || string(got) != string(original) {
					t.Fatalf("locked content survived failed mutator: %q (%v)", got, err)
				}
				if !a.LastTurnMutated() || a.mutations == 0 {
					t.Fatal("failed mutator's potential effects were not accounted for")
				}
			})
		}
	}
}

func TestNestedEditsApplyRulesAndDiagnostics(t *testing.T) {
	a, _ := newAgent(t, &fake{}, nil)
	a.AutoApprove = true
	a.Off = map[string]bool{"asyncdiag": true}
	rules, diagnoses := 0, 0
	a.PathRules = func([]string) []string { rules++; return []string{"follow this rule"} }
	a.Diagnose = func(context.Context, string) string { diagnoses++; return "diagnostic evidence" }
	out, failed, _ := a.runNested(context.Background(), "w", "write_file", json.RawMessage(`{"path":"a.txt","content":"changed"}`))
	if failed || rules != 1 || diagnoses != 1 || !strings.Contains(out, "diagnostic evidence") {
		t.Fatalf("nested lifecycle omitted checks: rules=%d diagnostics=%d failed=%v out=%q", rules, diagnoses, failed, out)
	}
}

type touchingFailureTool struct {
	funcTool
	path string
}

func (t touchingFailureTool) Touches(json.RawMessage) ([]string, error) {
	return []string{t.path}, nil
}

func TestFailedMutationsCapturePartialFileChanges(t *testing.T) {
	for _, nested := range []bool{false, true} {
		t.Run(fmtLifecycle(nested, false), func(t *testing.T) {
			a, root := newAgent(t, &fake{}, nil)
			a.AutoApprove = true
			path := filepath.Join(root, "a.txt")
			a.Tools = tool.NewRegistry(touchingFailureTool{funcTool: funcTool{name: "partial", mutating: true, run: func(json.RawMessage) (string, error) {
				if err := os.WriteFile(path, []byte("partial\n"), 0o644); err != nil {
					return "", err
				}
				return "", errors.New("failed after writing")
			}}, path: path})
			if nested {
				a.runNested(context.Background(), "p", "partial", json.RawMessage(`{}`))
			} else {
				a.execute(context.Background(), llm.Block{ID: "p", Name: "partial", Input: json.RawMessage(`{}`)}, func(Event) {})
			}
			changes := a.Changes()
			if len(changes) != 1 || string(changes[0].Before) != "alpha\n" || string(changes[0].After) != "partial\n" {
				t.Fatalf("partial effects missing from provenance: %+v", changes)
			}
		})
	}
}

func TestNestedEditsCheckDependenciesAndTyposquats(t *testing.T) {
	t.Setenv("JANETCODE_OFFLINE", "")
	a, _ := newAgent(t, &fake{}, nil)
	a.AutoApprove = true
	a.DepRegistry = fakeRegistry{}
	for _, tt := range []struct{ input, want string }{
		{`{"path":"requirements.txt","content":"requestz-helper\n"}`, "requestz-helper: not found"},
		{`{"path":"app.py","content":"import reqeusts\n"}`, "[typosquat check]"},
	} {
		out, failed, _ := a.runNested(context.Background(), "w", "write_file", json.RawMessage(tt.input))
		if failed || !strings.Contains(out, tt.want) {
			t.Fatalf("nested check missing %q: failed=%v out=%q", tt.want, failed, out)
		}
	}
}

func TestFailedAcceptanceEditTracksOnlyActualChanges(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(fmtLifecycle(true, changed), func(t *testing.T) {
			a, root := newAgent(t, &fake{}, nil)
			a.AutoApprove = true
			path := filepath.Join(root, "foo_test.go")
			a.SetAccept(&session.AcceptMeta{Phase: session.AcceptProposed, Root: root}, nil)
			a.Tools = tool.NewRegistry(touchingFailureTool{funcTool: funcTool{name: "write_file", mutating: true, run: func(json.RawMessage) (string, error) {
				if changed {
					if err := os.WriteFile(path, []byte("package foo\n"), 0o644); err != nil {
						return "", err
					}
				}
				return "", errors.New("write failed")
			}}, path: path})
			_, failed, denied := a.runNested(context.Background(), "w", "write_file", json.RawMessage(`{"path":"foo_test.go"}`))
			if !failed || denied {
				t.Fatalf("failed=%v denied=%v", failed, denied)
			}
			got := a.AcceptMeta()
			if changed && (got.Phase != session.AcceptWriting || !slices.Contains(got.Files, path)) {
				t.Fatalf("partial test write was not tracked: %+v", got)
			}
			if !changed && (got.Phase != session.AcceptProposed || len(got.Files) != 0) {
				t.Fatalf("failed write with no effects changed acceptance state: %+v", got)
			}
		})
	}
}

func TestCancelledBatchResultsAgreeAcrossSinks(t *testing.T) {
	a, _ := newAgent(t, &fake{}, nil)
	a.AutoApprove, a.Log = true, &session.Memory{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.Tools.Add(funcTool{name: "partial", mutating: true, run: func(json.RawMessage) (string, error) {
		cancel()
		return "", ctx.Err()
	}})
	uses := []llm.Block{
		{ID: "p", Name: "partial", Input: json.RawMessage(`{}`)},
		{ID: "r", Name: "read_file", Input: json.RawMessage(`{"path":"a.txt"}`)},
	}
	evs, emit := collect()
	results, _, err := a.executeAll(ctx, uses, emit)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	for i, u := range uses {
		count := 0
		for _, e := range *evs {
			if e.Kind == EvToolResult && e.Call.ID == u.ID {
				count++
				if e.Output != results[i].Text || e.IsError != results[i].IsError {
					t.Fatalf("result differs between event and history: %+v vs %+v", e, results[i])
				}
			}
		}
		if count != 1 {
			t.Fatalf("call %s got %d result events", u.ID, count)
		}
		count = 0
		for _, e := range a.logEvents() {
			if e.Type == session.TypeToolResult && e.CallID == u.ID {
				count++
				if e.Output != results[i].Text || e.IsError != results[i].IsError {
					t.Fatalf("result differs between log and history: %+v vs %+v", e, results[i])
				}
			}
		}
		if count != 1 {
			t.Fatalf("call %s got %d result log entries", u.ID, count)
		}
	}
}
