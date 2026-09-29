package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/snapshot"
	"github.com/xmonader/janetcode/internal/tool"
)

// A bash command names no paths, so before, the audit had no file/change
// record for what one changed. Now git status around the call finds the
// files it created, edited and deleted — including one that was already
// dirty — and records each against the call, while a file it did not
// touch gets nothing.
func TestBashFileChangesAreAudited(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("c1", "bash", `{"command":"echo made > made.txt; echo changed > a.txt; rm b.txt; echo more >> dirty.txt"}`),
		text("done"),
	}}
	a, root := newAgent(t, f, nil)
	os.WriteFile(filepath.Join(root, "b.txt"), []byte("b\n"), 0o644)
	os.WriteFile(filepath.Join(root, "dirty.txt"), []byte("d\n"), 0o644)
	os.WriteFile(filepath.Join(root, "still.txt"), []byte("s\n"), 0o644)
	gitInit(t, root)
	// Dirty before the turn, and dirty (differently) after it.
	os.WriteFile(filepath.Join(root, "dirty.txt"), []byte("d2\n"), 0o644)
	os.WriteFile(filepath.Join(root, "untouched-untracked.txt"), []byte("u\n"), 0o644)
	repo, err := snapshot.Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	a.Snapshots = repo
	a.AutoApprove = true
	a.Tools = tool.NewRegistry(tool.Bash{Root: root})
	sess, err := session.New(t.TempDir(), root, "p", "m")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.OpenSession(sess, nil); err != nil {
		t.Fatal(err)
	}
	if err := a.Run(context.Background(), "change things", nil); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, e := range sess.Events() {
		if e.Type == session.TypeFileChange {
			if e.CallID != "c1" {
				t.Errorf("change %s recorded against %q", e.Name, e.CallID)
			}
			got[filepath.Base(e.Name)] = e.Arguments
		}
	}
	d := func(s string) string { return contentDigest([]byte(s), true) }
	want := map[string]string{
		"made.txt":  "absent->" + d("made\n"),
		"a.txt":     d("alpha\n") + "->" + d("changed\n"),
		"b.txt":     d("b\n") + "->absent",
		"dirty.txt": d("d2\n") + "->" + d("d2\nmore\n"),
	}
	for name, w := range want {
		if got[name] != w {
			t.Errorf("%s: %q, want %q", name, got[name], w)
		}
	}
	if len(got) != len(want) {
		t.Errorf("records = %v, want only %v", got, want)
	}
	if rep, err := session.VerifyAudit(sess.Path()); err != nil || !rep.OK() {
		t.Fatalf("audit = %+v, %v", rep, err)
	}
}

type unattended struct{}

func (unattended) Approve(context.Context, ToolCall) (Decision, error) { return Deny, nil }
func (unattended) Unattended() bool                                    { return true }

// A run with no terminal denies by itself; the audit says so instead of
// crediting a person with the refusal.
func TestUnattendedRefusalIsNotTheUsers(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("c1", "write_file", `{"path":"x.txt","content":"x"}`),
		text("done"),
	}}
	a, root := newAgent(t, f, unattended{})
	sess, err := session.New(t.TempDir(), root, "p", "m")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.OpenSession(sess, nil); err != nil {
		t.Fatal(err)
	}
	if err := a.Run(context.Background(), "write", func(Event) {}); err != nil {
		t.Fatal(err)
	}
	for _, e := range sess.Events() {
		if e.Type == session.TypePermission {
			if !e.Denied || !strings.HasPrefix(e.Reason, "headless: no one to ask") {
				t.Fatalf("permission = %+v", e)
			}
			return
		}
	}
	t.Fatal("no permission record")
}
