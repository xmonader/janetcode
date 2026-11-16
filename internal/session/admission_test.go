package session

import (
	"reflect"
	"testing"

	"github.com/xmonader/janetcode/internal/admit"
	"github.com/xmonader/janetcode/internal/llm"
)

// The file admission policy is part of what a session works under: a
// resume and a fork keep the mode and both lists, and a session returned
// to the default mode records nothing.
func TestAdmissionSurvivesResumeAndFork(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir, "/work", "p", "m")
	if err != nil {
		t.Fatal(err)
	}
	put(t, s, llm.TextMessage(llm.RoleUser, "hi"))
	want := admit.State{Mode: admit.Explicit, Edit: []string{"src/", "main.go"}, Attach: []string{"README.md"}}
	if err := s.SetAdmission(want); err != nil {
		t.Fatal(err)
	}
	re, _, err := Open(dir, s.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if re.Meta.Admission == nil || !reflect.DeepEqual(*re.Meta.Admission, want) {
		t.Fatalf("resumed admission = %+v, want %+v", re.Meta.Admission, want)
	}
	f, err := Fork(dir, s, "")
	if err != nil {
		t.Fatal(err)
	}
	rf, _, err := Open(dir, f.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rf.Meta.Admission == nil || !reflect.DeepEqual(*rf.Meta.Admission, want) {
		t.Fatalf("forked admission = %+v, want %+v", rf.Meta.Admission, want)
	}
	if err := s.SetAdmission(admit.State{Mode: admit.Workspace}); err != nil {
		t.Fatal(err)
	}
	if s.Meta.Admission != nil {
		t.Errorf("a default policy left a record: %+v", s.Meta.Admission)
	}
}
