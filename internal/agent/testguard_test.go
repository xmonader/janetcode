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
