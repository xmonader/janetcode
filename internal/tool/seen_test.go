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

// Each reason a file is not what the agent last saw gets its own message
// and its own instruction, and the instruction differs by verb.
func TestPriorReadCausesAreDistinct(t *testing.T) {
	root := t.TempDir()
	seen := &Seen{}
	edit := EditFile{Root: root, Seen: seen}
	write := WriteFile{Root: root, Seen: seen}
	read := ReadFile{Root: root, Seen: seen}
	ctx := context.Background()

	// Never read, and it already exists: a write would replace unseen text.
	p := filepath.Join(root, "b.go")
	os.WriteFile(p, []byte("package b\n"), 0o644)
	_, err := write.Run(ctx, json.RawMessage(`{"path":"b.go","content":"x"}`))
	var pr *ErrPriorRead
	if !errors.As(err, &pr) || pr.Cause != CauseNeverRead {
		t.Fatalf("write of an unread file: %v", err)
	}
	if !strings.Contains(err.Error(), "whole file") {
		t.Fatalf("write guidance should ask for the whole file: %v", err)
	}
	// An edit of the same unread file is still fine: it names what it replaces.
	if _, err := edit.Run(ctx, json.RawMessage(`{"path":"b.go","old_string":"package b","new_string":"package c"}`)); err != nil {
		t.Fatalf("edit of an unread file: %v", err)
	}

	// Changed since read: the edit guidance allows a partial read.
	if _, err := read.Run(ctx, json.RawMessage(`{"path":"b.go"}`)); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(p, []byte("package d\n"), 0o644)
	_, err = edit.Run(ctx, json.RawMessage(`{"path":"b.go","old_string":"package d","new_string":"package e"}`))
	if !errors.As(err, &pr) || pr.Cause != CauseChanged {
		t.Fatalf("changed: %v", err)
	}
	if !strings.Contains(err.Error(), "partial read") {
		t.Fatalf("edit guidance should allow a partial read: %v", err)
	}

	// Disappeared: not the same message as changed.
	os.Remove(p)
	_, err = edit.Run(ctx, json.RawMessage(`{"path":"b.go","old_string":"a","new_string":"b"}`))
	if !errors.As(err, &pr) || pr.Cause != CauseDisappeared {
		t.Fatalf("disappeared: %v", err)
	}
	if !strings.Contains(err.Error(), "deleted, moved or renamed") {
		t.Fatalf("disappeared message: %v", err)
	}

	// A directory is named as one, whatever the agent has read.
	os.Mkdir(filepath.Join(root, "d"), 0o755)
	_, err = write.Run(ctx, json.RawMessage(`{"path":"d","content":"x"}`))
	if !errors.As(err, &pr) || pr.Cause != CauseDirectory {
		t.Fatalf("directory: %v", err)
	}

	// Binary: read as text earlier, NUL bytes now.
	bp := filepath.Join(root, "c.dat")
	os.WriteFile(bp, []byte("text\n"), 0o644)
	if _, err := read.Run(ctx, json.RawMessage(`{"path":"c.dat"}`)); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(bp, []byte("bin\x00ary\n"), 0o644)
	_, err = edit.Run(ctx, json.RawMessage(`{"path":"c.dat","old_string":"bin","new_string":"BIN"}`))
	if !errors.As(err, &pr) || pr.Cause != CauseBinary {
		t.Fatalf("binary: %v", err)
	}
	if !strings.Contains(err.Error(), "cannot be edited") {
		t.Fatalf("binary edit message: %v", err)
	}
	// The verb is conjugated, not suffixed: "writeed" is what "%sed" made
	// of a write.
	if msg := (&ErrPriorRead{Path: "c.dat", Verb: "write", Cause: CauseBinary}).Error(); !strings.Contains(msg, "cannot be written") {
		t.Fatalf("binary write message: %q", msg)
	}
}
