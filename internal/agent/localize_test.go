package agent

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/localize"
	"github.com/xmonader/janetcode/internal/localize/tree"
	"github.com/xmonader/janetcode/internal/tool"
	"github.com/xmonader/janetcode/internal/verify"
)

func TestLocalizeStateClaimsOncePerRedStateAndBoundsATurn(t *testing.T) {
	var s localizeState
	if !s.claim(1, "a") || s.claim(1, "a") {
		t.Fatal("the same red state was localized twice")
	}
	for _, k := range []string{"b", "c"} {
		if !s.claim(1, k) {
			t.Fatalf("claim %s refused under the cap", k)
		}
	}
	if s.claim(1, "d") {
		t.Fatalf("a turn localized more than %d times", maxLocalizePerTurn)
	}
	if !s.claim(2, "a") {
		t.Fatal("a new turn starts with a clean slate")
	}
}

func TestLocalizeKeyIgnoresTimings(t *testing.T) {
	edits := []localize.Edit{{Path: "a.go", After: []byte("x")}}
	k1 := localizeKey(edits, "--- FAIL: TestA (0.01s)\nFAIL\tm\t0.20s\n")
	k2 := localizeKey(edits, "--- FAIL: TestA (0.03s)\nFAIL\tm\t0.41s\n")
	if k1 != k2 {
		t.Fatal("two runs of the same failure keyed differently")
	}
	if k1 == localizeKey([]localize.Edit{{Path: "a.go", After: []byte("y")}}, "--- FAIL: TestA (0.01s)\n") {
		t.Fatal("different edits keyed the same")
	}
}

const locCalcBefore = `package calc

// Add adds.
func Add(a, b int) int {
	return a + b
}

// Mul multiplies.
//
// It is the one the tests
// catch when it goes wrong.
func Mul(a, b int) int {
	return a * b
}
`

const locCalcAfter = `package calc

// Add adds two numbers.
func Add(a, b int) int {
	return a + b
}

// Mul multiplies.
//
// It is the one the tests
// catch when it goes wrong.
func Mul(a, b int) int {
	return a + b
}
`

// localizeAgent is an agent in a committed Go module whose turn 1 has
// rewritten calc.go with two hunks, one of them breaking TestMul.
func localizeAgent(t *testing.T) (*Agent, string) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not installed")
	}
	a, root := newAgent(t, &fake{}, nil)
	a.Root = root
	for rel, content := range map[string]string{
		"go.mod":            "module example.com/m\n\ngo 1.21\n",
		"calc/calc.go":      locCalcBefore,
		"calc/calc_test.go": "package calc\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(1, 2) != 3 {\n\t\tt.Fatal(\"add\")\n\t}\n}\n\nfunc TestMul(t *testing.T) {\n\tif Mul(2, 3) != 6 {\n\t\tt.Fatal(\"mul\")\n\t}\n}\n",
	} {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitInit(t, root)
	a.Tools = tool.NewRegistry(tool.WriteFile{Root: root}, tool.Bash{Root: root})
	a.Localize = tree.Run
	a.AutoApprove = true
	a.turnN = 1
	a.cp.beginTurn(1, len(a.Messages))
	in, _ := json.Marshal(map[string]string{"path": filepath.Join(root, "calc/calc.go"), "content": locCalcAfter})
	if _, _, isErr, err := a.executeTool(context.Background(), llm.Block{Type: llm.BlockToolUse, ID: "w1", Name: "write_file", Input: in}, func(Event) {}); err != nil || isErr {
		t.Fatalf("write: %v %v", isErr, err)
	}
	return a, root
}

func runBash(t *testing.T, a *Agent, id, command string) string {
	t.Helper()
	in, _ := json.Marshal(map[string]string{"command": command})
	out, _, _, err := a.executeTool(context.Background(), llm.Block{Type: llm.BlockToolUse, ID: id, Name: "bash", Input: in}, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestRedGoTestAfterAMultiHunkEditNamesTheHunk(t *testing.T) {
	if testing.Short() {
		t.Skip("runs go test")
	}
	a, root := localizeAgent(t)
	edits := a.turnEdits()
	if len(edits) != 1 || edits[0].Path != "calc/calc.go" || string(edits[0].Before) != locCalcBefore || string(edits[0].After) != locCalcAfter {
		t.Fatalf("turn edits = %+v", edits)
	}

	// Off (the default): the red run comes back as it was.
	if out := runBash(t, a, "b1", "go test ./..."); strings.Contains(out, "[localize]") || !strings.Contains(out, "--- FAIL: TestMul") {
		t.Fatalf("localized while off, or the run was not red:\n%s", out)
	}

	a.SetLocalizeFailures(true)
	out := runBash(t, a, "b2", "go test ./...")
	for _, want := range []string{"[localize] Of the 2 hunks", "TestMul", "hunk 2 of 2: calc/calc.go line 13", "Mul (failing tests ran it 1, passing 0), edited this turn"} {
		if !strings.Contains(out, want) {
			t.Errorf("result lacks %q:\n%s", want, out)
		}
	}
	// The same red state again is not worked out a second time.
	if out := runBash(t, a, "b3", "go test ./..."); strings.Contains(out, "[localize]") {
		t.Fatalf("the same red state was localized twice:\n%s", out)
	}
	// A green run, and a command that is not a check, get nothing.
	if err := os.WriteFile(filepath.Join(root, "calc/calc.go"), []byte(strings.Replace(locCalcAfter, "Mul(a, b int) int {\n\treturn a + b", "Mul(a, b int) int {\n\treturn a * b", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if out := runBash(t, a, "b4", "go test ./..."); strings.Contains(out, "[localize]") || !strings.Contains(out, "ok") {
		t.Fatalf("a green run was localized:\n%s", out)
	}
	if out := runBash(t, a, "b5", "echo '--- FAIL: TestMul'; exit 1"); strings.Contains(out, "[localize]") {
		t.Fatalf("a non-check command was localized:\n%s", out)
	}
}

func TestOneHunkIsNotLocalized(t *testing.T) {
	a, root := newAgent(t, &fake{}, nil)
	a.Root = root
	a.SetLocalizeFailures(true)
	a.Localize = func(context.Context, localize.Request) localize.Report {
		t.Fatal("localized a single hunk")
		return localize.Report{}
	}
	a.turnN = 1
	p := filepath.Join(root, "a.txt")
	a.record(Change{Turn: 1, Path: p, Existed: true, Before: []byte("alpha\n"), After: []byte("beta\n")})
	if err := os.WriteFile(p, []byte("beta\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := a.localizeFailure(context.Background(), "--- FAIL: TestA (0.00s)\nFAIL\tm\t0.1s\n", func(Event) {}); got != "" {
		t.Fatalf("one hunk localized: %q", got)
	}
	// Outside the root, or undone: not the turn's edits.
	a.record(Change{Turn: 1, Path: "/elsewhere/x.go", Existed: true, Before: []byte("a\n"), After: []byte("b\n")})
	a.record(Change{Turn: 1, Path: filepath.Join(root, "u.txt"), Before: nil, After: []byte("x\n"), Undone: true})
	a.record(Change{Turn: 0, Path: filepath.Join(root, "old.txt"), Before: nil, After: []byte("x\n")})
	if edits := a.turnEdits(); len(edits) != 1 || edits[0].Path != "a.txt" {
		t.Fatalf("turn edits = %+v", edits)
	}
}

func TestGateFailureCarriesTheLocalization(t *testing.T) {
	if testing.Short() {
		t.Skip("runs go test")
	}
	a, root := localizeAgent(t)
	a.Verify = &verify.Runner{Cwd: root, Config: verify.Config{Commands: []string{"go test ./..."}}}
	a.SetLocalizeFailures(true)
	var g gateState
	if !a.verifyGate(context.Background(), &g, func(Event) {}) {
		t.Fatal("the red gate did not send the turn back")
	}
	last := a.Messages[len(a.Messages)-1].Text()
	for _, want := range []string{"Verification failed", "[localize] Of the 2 hunks", "hunk 2 of 2: calc/calc.go line 13"} {
		if !strings.Contains(last, want) {
			t.Errorf("gate feedback lacks %q:\n%s", want, last)
		}
	}
}
