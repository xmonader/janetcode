package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/xmonader/janetcode/internal/tool"
)

// Embedding the base interface hides optional Rich support.
type textOnlyRangeTool struct{ tool.Tool }

func TestRangeReadPreservesStructuredDataAndPatternLog(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	log := &patternLog{dir: root}
	glob := rangeRead{Tool: tool.Glob{Root: root}, log: log}
	r, err := glob.RunRich(context.Background(), json.RawMessage(`{"pattern":"*.go"}`))
	if err != nil {
		t.Fatal(err)
	}
	var files tool.GlobData
	if err := json.Unmarshal(r.Data, &files); err != nil || len(files.Paths) != 1 || files.Paths[0] != "a.go" || r.Text != "a.go\n" {
		t.Fatalf("glob=%+v data=%s err=%v", r, r.Data, err)
	}
	grep := rangeRead{Tool: tool.Grep{Root: root}, log: log}
	r, err = grep.RunRich(context.Background(), json.RawMessage(`{"pattern":"package"}`))
	if err != nil {
		t.Fatal(err)
	}
	var matches tool.GrepData
	if err := json.Unmarshal(r.Data, &matches); err != nil || len(matches.Matches) != 1 || matches.Matches[0].Text != "package main" || r.Text != "a.go:1:package main\n" {
		t.Fatalf("grep=%+v data=%s err=%v", r, r.Data, err)
	}
	if _, err := grep.RunRich(context.Background(), json.RawMessage(`{"pattern":"["}`)); err == nil {
		t.Fatal("invalid search succeeded")
	}
	reads := log.reads()
	if len(reads) != 2 || reads[0].Tool != "glob" || reads[0].Pattern != "*.go" || reads[1].Tool != "grep" || reads[1].Pattern != "package" {
		t.Fatalf("successful range-read log=%+v", reads)
	}
}

func TestRangeReadRichFallsBackToText(t *testing.T) {
	root := t.TempDir()
	log := &patternLog{dir: root}
	r := rangeRead{Tool: textOnlyRangeTool{Tool: tool.Glob{Root: root}}, log: log}
	out, err := r.RunRich(context.Background(), json.RawMessage(`{"pattern":"*.go"}`))
	if err != nil || out.Text != "No files matched." || len(out.Data) != 0 || len(log.reads()) != 1 {
		t.Fatalf("result=%+v reads=%+v err=%v", out, log.reads(), err)
	}
}
