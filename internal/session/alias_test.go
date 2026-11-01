package session

import (
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

func TestAppendDetachesMessagesFromTheCaller(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir, "/w", "p", "m")
	if err != nil {
		t.Fatal(err)
	}
	msg := llm.TextMessage(llm.RoleUser, "original")
	put(t, s, msg)
	msg.Blocks[0].Text = "changed by caller"

	events := s.Events()
	if got := events[0].Message.Text(); got != "original" {
		t.Fatalf("stored event changed through the caller's blocks: %q", got)
	}
	if err := s.SetTitle("rewrite"); err != nil {
		t.Fatal(err)
	}
	_, msgs, err := Open(dir, s.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Text() != "original" {
		t.Fatalf("metadata rewrite persisted a caller mutation: %+v", msgs)
	}
	if report, err := VerifyAudit(s.Path()); err != nil || !report.OK() {
		t.Fatalf("audit after rewrite: %+v, %v", report, err)
	}
}

func TestFoldSurfaceDetachesMessagesFromTheLog(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir, "/w", "p", "m")
	if err != nil {
		t.Fatal(err)
	}
	put(t, s, llm.TextMessage(llm.RoleUser, "original"))
	reopened, msgs, err := Open(dir, s.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	msgs[0].Blocks[0].Text = "changed by caller"

	events := reopened.Events()
	if got := events[0].Message.Text(); got != "original" {
		t.Fatalf("stored event changed through the folded messages: %q", got)
	}
	if err := reopened.SetTitle("rewrite"); err != nil {
		t.Fatal(err)
	}
	_, msgs, err = Open(dir, s.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Text() != "original" {
		t.Fatalf("metadata rewrite persisted a folded-message mutation: %+v", msgs)
	}
	if report, err := VerifyAudit(s.Path()); err != nil || !report.OK() {
		t.Fatalf("audit after rewrite: %+v, %v", report, err)
	}
}
