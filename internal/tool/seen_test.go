package tool

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEditRefusedWhenFileChangedSinceRead(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "a.go")
	os.WriteFile(p, []byte("package a\n\nfunc A() {}\n"), 0o644)
	seen := &Seen{}
	read := ReadFile{Root: root, Seen: seen}
	edit := EditFile{Root: root, Seen: seen}
	write := WriteFile{Root: root, Seen: seen}
	ctx := context.Background()

	// Never read: edits are allowed (the coach handles unread files).
	if _, err := edit.Run(ctx, json.RawMessage(`{"path":"a.go","old_string":"A()","new_string":"B()"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := read.Run(ctx, json.RawMessage(`{"path":"a.go"}`)); err != nil {
		t.Fatal(err)
	}
	// Someone else changes the file behind the agent's back.
	os.WriteFile(p, []byte("package a\n\nfunc B() { /* human */ }\n"), 0o644)
	_, err := edit.Run(ctx, json.RawMessage(`{"path":"a.go","old_string":"B()","new_string":"C()"}`))
	var ch *ErrChanged
	if !errors.As(err, &ch) || !strings.Contains(err.Error(), "a.go changed on disk") {
		t.Fatalf("edit err = %v", err)
	}
	if data, _ := os.ReadFile(p); !strings.Contains(string(data), "human") {
		t.Fatal("refused edit still wrote the file")
	}
	if _, err := write.Run(ctx, json.RawMessage(`{"path":"a.go","content":"x"}`)); !errors.As(err, &ch) {
		t.Fatalf("write err = %v", err)
	}
	// Reading again re-arms the agent; its own writes keep the record fresh.
	if _, err := read.Run(ctx, json.RawMessage(`{"path":"a.go"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := edit.Run(ctx, json.RawMessage(`{"path":"a.go","old_string":"B()","new_string":"C()"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := write.Run(ctx, json.RawMessage(`{"path":"a.go","content":"package a\n"}`)); err != nil {
		t.Fatal(err)
	}
	// A deleted file counts as changed.
	os.Remove(p)
	if _, err := write.Run(ctx, json.RawMessage(`{"path":"a.go","content":"y"}`)); !errors.As(err, &ch) {
		t.Fatalf("write after delete err = %v", err)
	}
	seen.Forget(p)
	if _, err := write.Run(ctx, json.RawMessage(`{"path":"a.go","content":"y"}`)); err != nil {
		t.Fatal(err)
	}
	// A nil Seen disables everything.
	if _, err := (EditFile{Root: root}).Run(ctx, json.RawMessage(`{"path":"a.go","old_string":"y","new_string":"z"}`)); err != nil {
		t.Fatal(err)
	}
}
