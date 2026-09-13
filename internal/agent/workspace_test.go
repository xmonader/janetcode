package agent

import (
	"path/filepath"
	"testing"

	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

func TestAddDirRequiresAWorkspace(t *testing.T) {
	if err := AddDir(nil, nil, t.TempDir()); err == nil {
		t.Fatal("want error with no workspace")
	}
}

func TestAddDirRejectsAMissingPath(t *testing.T) {
	ws := tool.NewLocal(t.TempDir())
	if err := AddDir(ws, nil, filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("want error for a directory that does not exist")
	}
}

func TestAddDirGrantsTheDirectory(t *testing.T) {
	ws := tool.NewLocal(t.TempDir())
	dir := t.TempDir()
	if err := AddDir(ws, nil, dir); err != nil {
		t.Fatal(err)
	}
	dirs := ws.ExtraDirs()
	if len(dirs) != 1 {
		t.Fatalf("extra dirs = %v", dirs)
	}
}

func TestAddDirPersistsOnTheSession(t *testing.T) {
	tmp := t.TempDir()
	sess, err := session.New(tmp, tmp, "provider", "model")
	if err != nil {
		t.Fatal(err)
	}
	ws := tool.NewLocal(t.TempDir())
	dir := t.TempDir()
	if err := AddDir(ws, sess, dir); err != nil {
		t.Fatal(err)
	}
	if got := sess.Meta.ExtraDirs; len(got) != 1 {
		t.Fatalf("session extra dirs = %v", got)
	}
}
