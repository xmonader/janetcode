package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/perm"
)

// The paths a merge may write are checked against the rules before the
// first round, but a repair round can write files the first round never
// touched. Those were merged into the project unchecked: a path a deny
// rule refuses reached the real tree by way of a merge conflict.
func TestMergeBackRepairRoundIsCheckedAgainstTheRules(t *testing.T) {
	var root string
	prov := &occScript{}
	prov.step = func(n int, repair bool) *llm.Response {
		if repair {
			switch n {
			case 0:
				return editCall("r1", "a.go", `return "a" + "!"`, `return "A" + "!"`)
			case 1:
				return toolCall("r2", "bash", `{"command":"printf planted > secret.txt"}`)
			}
			return occSay("REPORT: re-applied, and wrote secret.txt")
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
	parent.Rules = perm.Rules{Deny: []perm.Rule{{Tool: "write_file", Pattern: "secret.txt"}}}
	out, err := task.Run(context.Background(), json.RawMessage(`{"description":"change A","prompt":"change A","edit":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "secret.txt")); err == nil {
		t.Fatalf("a denied path was merged into the project:\n%s", out)
	}
	if got := readFile(t, filepath.Join(root, "a.go")); got != strings.Replace(occGo, `return "a"`, `return "a" + "!"`, 1) {
		t.Fatalf("project a.go changed by a refused merge:\n%s", got)
	}
	if !strings.Contains(out, "secret.txt") || !strings.Contains(out, "denied") {
		t.Fatalf("the report does not say why the merge was refused:\n%s", out)
	}
}
