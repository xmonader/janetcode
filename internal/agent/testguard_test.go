package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/tool"
)

// The first write to a test file is refused with a note to check inline;
// the same write again goes through; other files are not touched by it.
func TestTestGuardRefusesTheFirstTestWrite(t *testing.T) {
	root := t.TempDir()
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"test_mine.py","content":"x\n"}`),
		toolCall("t2", "write_file", `{"path":"test_mine.py","content":"x\n"}`),
		toolCall("t3", "write_file", `{"path":"lib.py","content":"y\n"}`),
		text("done"),
	}}
	a := &Agent{Provider: f, Model: "m", AutoApprove: true, TestGuard: true,
		Tools: tool.NewRegistry(tool.WriteFile{Root: root})}
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if got := diagLastText(f.requests[1]); !strings.Contains(got, "inline command") {
		t.Fatalf("first write's result = %q", got)
	}
	for _, name := range []string{"test_mine.py", "lib.py"} {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Fatalf("%s was not written: %v", name, err)
		}
	}

	b := &Agent{TestGuard: true, Off: map[string]bool{"testguard": true}}
	if b.testFileDenial(llm.Block{Name: "write_file", Input: []byte(`{"path":"test_a.py"}`)}) != "" {
		t.Fatal("refused with the switch off")
	}
	c := &Agent{TestGuard: true}
	if c.testFileDenial(llm.Block{Name: "edit_file", Input: []byte(`{"path":"pkg/a_test.go"}`)}) == "" {
		t.Fatal("a Go test edit was not refused")
	}
}

// A request that asks for tests is not guarded: the refusal would only
// cost a model round before the same write went through.
func TestTestGuardStandsAsideWhenTheTaskAsksForTests(t *testing.T) {
	edit := llm.Block{Name: "edit_file", Input: []byte(`{"path":"pkg/a_test.go"}`)}
	for _, req := range []string{"Fix it and add tests.", "Add a regression test.", "fix the bug, adding test cases for both paths"} {
		a := &Agent{TestGuard: true}
		a.integ.userText = req
		if r := a.testFileDenial(edit); r != "" {
			t.Errorf("%q: refused", req)
		}
	}
	a := &Agent{TestGuard: true}
	a.integ.userText = "Fix the bug. Do not modify invoice_test.go."
	if a.testFileDenial(edit) == "" {
		t.Error("a request that asks for no tests was not guarded")
	}
}
