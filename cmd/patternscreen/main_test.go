package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/xmonader/janetcode/internal/editpattern"
)

func patternscreenWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestJanetcodeEditsReadsSessionLog(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "janetcode-sessions")
	patternscreenWrite(t, filepath.Join(dir, "20261009-000000-abcdef.jsonl"),
		`{"type":"meta","meta":{"id":"20261009-000000-abcdef","cwd":"/w","created":"2026-10-09T00:00:00Z"}}`+"\n"+
			`{"type":"event","seq":1,"at":"2026-10-09T00:00:01Z","t":"assistant/message","message":{"role":"assistant","blocks":[`+
			`{"type":"tool_use","id":"t1","name":"edit_file","input":{"path":"a.py","old_string":"item(sku)","new_string":"lookup(sku)"}}]}}`+"\n")
	got, err := janetcodeEdits(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != (editpattern.Edit{Path: "a.py", Old: "item(sku)", New: "lookup(sku)"}) {
		t.Fatalf("edits = %+v", got)
	}
	if _, err := janetcodeEdits(t.TempDir()); err == nil {
		t.Fatal("an empty directory should be an error")
	}
}

func TestPiEditsReadsEditList(t *testing.T) {
	dir := t.TempDir()
	patternscreenWrite(t, filepath.Join(dir, "s.jsonl"),
		`{"type":"session","id":"x"}`+"\n"+
			`{"type":"message","message":{"role":"assistant","content":[`+
			`{"type":"toolCall","id":"1","name":"edit","arguments":{"path":"a.py","edits":[{"oldText":"a","newText":"b"},{"oldText":"","newText":"c"}]}},`+
			`{"type":"toolCall","id":"2","name":"read","arguments":{"path":"a.py"}}]}}`+"\n"+
			`not json "toolCall"`+"\n")
	got, err := piEdits(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != (editpattern.Edit{Path: "a.py", Old: "a", New: "b"}) {
		t.Fatalf("edits = %+v", got)
	}
}
