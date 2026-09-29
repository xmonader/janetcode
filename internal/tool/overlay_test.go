package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestOverlayCatchesWritesAndServesReads: with an overlay set, the file
// tools write into it, read back from it and list what it created, and
// the disk is untouched; without it they reach the disk again.
func TestOverlayCatchesWritesAndServesReads(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "a.txt")
	if err := os.WriteFile(path, []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws := NewLocal(root)
	o := NewOverlay()
	ws.SetOverlay(o)
	ctx := context.Background()
	if _, err := (EditFile{WS: ws}).Run(ctx, json.RawMessage(`{"path":"a.txt","old_string":"one","new_string":"two"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := (WriteFile{WS: ws}).Run(ctx, json.RawMessage(`{"path":"sub/new.txt","content":"fresh\n"}`)); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(path); string(data) != "one\n" {
		t.Fatalf("the overlay let a write through: %q", data)
	}
	if _, err := os.Stat(filepath.Join(root, "sub")); !os.IsNotExist(err) {
		t.Fatal("the overlay created a directory on disk")
	}
	out, err := (ReadFile{WS: ws}).Run(ctx, json.RawMessage(`{"path":"a.txt"}`))
	if err != nil || !strings.Contains(out, "two") {
		t.Fatalf("read_file did not see the overlay: %q, %v", out, err)
	}
	list, err := (ListDir{WS: ws}).Run(ctx, json.RawMessage(`{"path":"sub"}`))
	if err != nil || !strings.Contains(list, "new.txt") {
		t.Fatalf("list_dir did not see the overlay's new directory: %q, %v", list, err)
	}
	if ch := o.Changes(); len(ch) != 2 || ch[0].Path != path || string(ch[0].Data) != "two\n" {
		t.Fatalf("changes = %+v", ch)
	}
	ws.SetOverlay(nil)
	out, _ = (ReadFile{WS: ws}).Run(ctx, json.RawMessage(`{"path":"a.txt"}`))
	if strings.Contains(out, "two") {
		t.Fatal("the overlay outlived SetOverlay(nil)")
	}
}
