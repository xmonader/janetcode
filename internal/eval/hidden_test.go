package eval

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// heldOutSecret is a string that appears only in the held-out test
// source. Every leak check below looks for exactly this.
const heldOutSecret = "SECRET_HELD_OUT_MARKER_7b21"

// hiddenTask is a tiny Go module whose visible test covers the happy
// path only, with a held-out test that also pins the empty case — the
// shape every task in evals/hidden has.
func hiddenTask(t *testing.T, impl string) Task {
	t.Helper()
	return Task{
		Name:   "hidden-fixture",
		Prompt: "make Join work",
		Files: map[string]string{
			"go.mod": "module example.com/w\n\ngo 1.22\n",
			"w.go":   impl,
			"w_test.go": "package w\n\nimport \"testing\"\n\nfunc TestJoinHappy(t *testing.T) {\n" +
				"\tif got := Join([]string{\"a\", \"b\"}); got != \"a,b\" {\n\t\tt.Fatalf(\"got %q\", got)\n\t}\n}\n",
		},
		HiddenFiles: map[string]string{
			"acc_test.go": "package acceptance_test\n\nimport (\n\t\"testing\"\n\n\tw \"example.com/w\"\n)\n\n" +
				"// " + heldOutSecret + "\nfunc TestJoinEmpty(t *testing.T) {\n" +
				"\tif got := w.Join(nil); got != \"\" {\n\t\tt.Errorf(\"Join(nil) = %q, want the empty string\", got)\n\t}\n}\n",
		},
		Check: `"$EVAL_HIDDEN_GATE" "$EVAL_DIR" >/dev/null 2>&1`,
	}
}

const joinWrong = "package w\n\nimport \"strings\"\n\n" +
	"func Join(parts []string) string {\n\treturn strings.Join(parts, \",\") + \",\"\n}\n"

const joinRight = "package w\n\nimport \"strings\"\n\n" +
	"func Join(parts []string) string {\n\treturn strings.Join(parts, \",\")\n}\n"

// TestHeldOutGateSeparatesBaseFromReference is the instrument's own
// acceptance test: the untouched fixture must FAIL the held-out tests
// and the reference solution must PASS them. A held-out set that does
// not do both measures nothing.
func TestHeldOutGateSeparatesBaseFromReference(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles a Go module")
	}
	for _, tc := range []struct {
		name string
		impl string
		want bool
	}{
		{"untouched fixture fails", joinWrong, false},
		{"reference solution passes", joinRight, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			task := hiddenTask(t, tc.impl)
			ok, err := CheckAtBase(context.Background(), task)
			if err != nil {
				t.Fatal(err)
			}
			if ok != tc.want {
				t.Fatalf("held-out gate passed = %v, want %v", ok, tc.want)
			}
		})
	}
}

// TestHeldOutTestsNeverReachTheWorkspace is the leak-proofing test. The
// held-out sources must not be in the work directory before, during or
// after the gate runs, and the failure text the model is given must name
// the failing test without quoting its source.
func TestHeldOutTestsNeverReachTheWorkspace(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles a Go module")
	}
	task := hiddenTask(t, joinWrong)
	work := t.TempDir()
	if err := writeFiles(work, task.Files); err != nil {
		t.Fatal(err)
	}
	gate, cleanup, err := prepareHidden(task)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if strings.HasPrefix(gate, work) {
		t.Fatalf("the gate script %q lives inside the workspace %q", gate, work)
	}
	out, rerr := runShell(t, HiddenGateCommand(gate, work), work)
	if rerr == nil {
		t.Fatalf("the gate passed on the untouched fixture: %s", out)
	}
	if !strings.Contains(out, "TestJoinEmpty") {
		t.Fatalf("the failure does not name the failing test: %q", out)
	}
	if strings.Contains(out, heldOutSecret) || strings.Contains(out, "w.Join(nil)") {
		t.Fatalf("the held-out source leaked into the failure text: %q", out)
	}
	if strings.Contains(out, filepath.Dir(gate)) {
		t.Fatalf("the held-out directory leaked into the failure text: %q", out)
	}
	// Nothing the gate did survives in the workspace.
	grepTree(t, work, heldOutSecret)
	grepTree(t, work, "TestJoinEmpty")
	if _, err := os.Stat(filepath.Join(work, HiddenDir)); !os.IsNotExist(err) {
		t.Fatalf("the held-out package exists in the workspace: %v", err)
	}
}

// TestHiddenTaskValidation refuses the two shapes that would silently
// defeat the instrument.
func TestHiddenTaskValidation(t *testing.T) {
	if err := checkHiddenTask(Task{HiddenFiles: map[string]string{"sub/x_test.go": "x"}}); err == nil {
		t.Fatal("a held-out file with a path was accepted")
	}
	if err := checkHiddenTask(Task{
		HiddenFiles: map[string]string{"x_test.go": "x"},
		Files:       map[string]string{HiddenDir + "/y.go": "y"},
	}); err == nil {
		t.Fatal("a visible file inside the held-out package was accepted")
	}
	if err := checkHiddenTask(Task{HiddenCheck: "go test ./..."}); err == nil {
		t.Fatal("hidden_check without held-out files was accepted")
	}
	if err := checkHiddenTask(Task{HiddenFiles: map[string]string{"x_test.go": "x"}}); err != nil {
		t.Fatalf("a well-formed held-out task was refused: %v", err)
	}
}

// grepTree fails when needle appears anywhere under root.
func grepTree(t *testing.T, root, needle string) {
	t.Helper()
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		data, rerr := os.ReadFile(p)
		if rerr != nil {
			return nil
		}
		if strings.Contains(string(data), needle) {
			t.Fatalf("%q found in %s", needle, p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// runShell runs one command in dir and returns its combined output.
func runShell(t *testing.T, command, dir string) (string, error) {
	t.Helper()
	out, err := runCheck(context.Background(), Task{Check: command}, dir, filepath.Join(t.TempDir(), "out.txt"), "")
	return out, err
}

// hiddenSet is the shipped held-out task set.
const hiddenSet = "../../evals/hidden"

// TestHiddenSetIsDiscriminating is the shipped set's own gate: for every
// task, the fixture as shipped must FAIL its held-out tests and the
// reference solution must PASS them. A task that fails neither half is
// measuring nothing, and this is the only place that ever runs the
// reference — it is never written into a work directory.
func TestHiddenSetIsDiscriminating(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles a Go module per task")
	}
	tasks, err := Load(hiddenSet)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range tasks {
		if !task.Hidden() {
			continue
		}
		t.Run(task.Name, func(t *testing.T) {
			t.Parallel()
			if len(task.Reference) == 0 {
				t.Fatal("no reference solution; the held-out tests are unproven")
			}
			ok, err := CheckAtBase(context.Background(), task)
			if err != nil {
				t.Fatal(err)
			}
			if ok {
				t.Fatal("the untouched fixture passes its held-out tests")
			}
			solved := task
			solved.Files = map[string]string{}
			for k, v := range task.Files {
				solved.Files[k] = v
			}
			for k, v := range task.Reference {
				solved.Files[k] = v
			}
			ok, err = CheckAtBase(context.Background(), solved)
			if err != nil {
				t.Fatal(err)
			}
			if !ok {
				out, _ := runShell(t, `"$EVAL_HIDDEN_GATE" "$EVAL_DIR"`, t.TempDir())
				t.Fatalf("the reference solution fails its own held-out tests: %s", out)
			}
		})
	}
}

// A control's failure text must not tell the model how to fake the
// environment. The first version of 98 pointed at 127.0.0.1:59717, and a
// run read the README, wrote a listener called `indexservice`, bound the
// port and turned the environmental failure into a pass. The port is now
// 81: an unprivileged process cannot bind it, so the only way past that
// step is the one there is meant to be, which is none.
//
// TestControlTasksFailTheirGateEnvironmentally: the control tasks exist
// to give triage a gate failure that is NOT caused by the patch, so that
// a skip or a veto can be scored in both directions. Their value depends
// entirely on the gate failing at base for a reason no code change can
// fix, which is what this asserts — the project's own build and tests
// pass, and the step after them does not.
func TestControlTasksFailTheirGateEnvironmentally(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles a Go module per task")
	}
	tasks, err := Load(hiddenSet)
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, task := range tasks {
		if !contains(task.Tags, "control") {
			continue
		}
		seen++
		t.Run(task.Name, func(t *testing.T) {
			t.Parallel()
			var cfg struct {
				Commands []string `json:"commands"`
			}
			raw, ok := task.Files[".janetcode/verify.json"]
			if !ok {
				t.Fatal("a control task ships no verify.json")
			}
			if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
				t.Fatal(err)
			}
			if len(cfg.Commands) < 2 {
				t.Fatalf("commands = %v, want the project's own checks and an environmental step", cfg.Commands)
			}
			dir := t.TempDir()
			if err := writeFiles(dir, task.Files); err != nil {
				t.Fatal(err)
			}
			for i, command := range cfg.Commands {
				out, err := runShell(t, command, dir)
				last := i == len(cfg.Commands)-1
				if last && err == nil {
					t.Fatalf("the environmental step passed: %s", out)
				}
				if !last && err != nil {
					t.Fatalf("the project's own check %q failed at base: %s", command, out)
				}
			}
		})
	}
	if seen < 2 {
		t.Fatalf("found %d control tasks, want at least 2", seen)
	}
}
