package agent

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

// TestCheckpointListSaysWhatARestoreCovers: the list names each restore
// point, says it holds file copies only outside git, and marks one with
// a file it could not copy as incomplete, naming the file and why.
func TestCheckpointListSaysWhatARestoreCovers(t *testing.T) {
	old := maxCopyBytes
	maxCopyBytes = 1024
	defer func() { maxCopyBytes = old }()
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"a.txt","content":"one\n"}`),
		text("done"),
		toolCall("t2", "write_file", `{"path":"huge.log","content":"trimmed\n"}`),
		text("done"),
	}}
	a, root := newAgent(t, f, nil)
	a.AutoApprove = true
	if err := os.WriteFile(filepath.Join(root, "huge.log"), bytes.Repeat([]byte("x"), 4096), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"first", "second"} {
		if err := a.Run(context.Background(), p, nil); err != nil {
			t.Fatal(err)
		}
	}
	list := a.CheckpointList()
	if len(list) != 2 || list[0].Turn != 1 || list[1].Turn != 2 {
		t.Fatalf("list = %+v", list)
	}
	if !list[0].Complete || !list[0].Scoped || list[0].Tree || list[0].ID != "t1" {
		t.Fatalf("first = %+v", list[0])
	}
	if list[1].Complete || len(list[1].Gaps) != 1 || !strings.Contains(list[1].Gaps[0], "huge.log") {
		t.Fatalf("second = %+v", list[1])
	}
	sheet := FormatCheckpoints(root, list)
	for _, want := range []string{"t2", "file copies only", "incomplete", "✗ huge.log", "/undo turn <n>"} {
		if !strings.Contains(sheet, want) {
			t.Errorf("sheet lacks %q:\n%s", want, sheet)
		}
	}
	if strings.Index(sheet, "t2") > strings.Index(sheet, "t1 ") {
		t.Errorf("newest first:\n%s", sheet)
	}
}
