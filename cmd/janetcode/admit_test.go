package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/admit"
)

// A headless run sets the mode and the lists with flags alone: no prompt,
// no slash command. The session records them, and a later run that gives
// no flags (a --resume) comes back to the same policy.
func TestFileAdmissionFlagsAndResume(t *testing.T) {
	d, sess := buildTestDeps(t)
	os.MkdirAll(filepath.Join(d.cwd, "src"), 0o755)
	os.WriteFile(filepath.Join(d.cwd, "src", "a.go"), []byte("package src\n"), 0o644)
	os.WriteFile(filepath.Join(d.cwd, "README.md"), []byte("# r\n"), 0o644)
	d.fileMode, d.admit, d.attach = "explicit", []string{"src"}, []string{"README.md"}

	b := newAgentBuilder(d, sess, nil)
	if err := b.stepWorkspace(); err != nil {
		t.Fatalf("stepWorkspace: %v", err)
	}
	pol := b.ws.Admission()
	edit, attach := pol.Lines()
	if !pol.Explicit() || len(edit) != 1 || edit[0] != "src/" || len(attach) != 1 || attach[0] != "README.md" {
		t.Fatalf("mode=%s edit=%v attach=%v", pol.Mode(), edit, attach)
	}
	if sess.Meta.Admission == nil || sess.Meta.Admission.Mode != admit.Explicit {
		t.Fatalf("session did not record the policy: %+v", sess.Meta.Admission)
	}

	// The resume: same session record, no flags.
	d2, _ := buildTestDeps(t)
	d2.cwd = d.cwd
	b2 := newAgentBuilder(d2, sess, nil)
	if err := b2.stepWorkspace(); err != nil {
		t.Fatalf("resume stepWorkspace: %v", err)
	}
	edit2, attach2 := b2.ws.Admission().Lines()
	if !b2.ws.Admission().Explicit() || len(edit2) != 1 || len(attach2) != 1 {
		t.Fatalf("resumed policy: mode=%s edit=%v attach=%v", b2.ws.Admission().Mode(), edit2, attach2)
	}

	// --file-mode on the resume overrides what the session remembered.
	d3, _ := buildTestDeps(t)
	d3.cwd, d3.fileMode = d.cwd, "workspace"
	b3 := newAgentBuilder(d3, sess, nil)
	if err := b3.stepWorkspace(); err != nil {
		t.Fatal(err)
	}
	if b3.ws.Admission().Explicit() {
		t.Error("--file-mode workspace did not override the remembered mode")
	}
}

// A path or mode named on the command line is the user asking right now,
// so one that cannot be honoured stops the run instead of starting it
// with a policy nobody wanted.
func TestFileAdmissionFlagErrors(t *testing.T) {
	for name, set := range map[string]func(d *agentDeps){
		"bad mode":        func(d *agentDeps) { d.fileMode = "strict" },
		"outside the dir": func(d *agentDeps) { d.admit = []string{filepath.Join(os.TempDir(), "elsewhere.txt")} },
		"attach outside":  func(d *agentDeps) { d.attach = []string{"../nope.txt"} },
	} {
		d, sess := buildTestDeps(t)
		set(&d)
		b := newAgentBuilder(d, sess, nil)
		if err := b.stepWorkspace(); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// The full build hands the policy to the agent, so its gate and its
// system prompt read the same list the file tools do.
func TestBuildAgentSharesTheWorkspacePolicy(t *testing.T) {
	d, sess := buildTestDeps(t)
	os.WriteFile(filepath.Join(d.cwd, "a.go"), []byte("package a\n"), 0o644)
	d.fileMode, d.admit = "explicit", []string{"a.go"}
	built, err := buildAgent(d, sess, nil)
	if err != nil {
		t.Fatalf("buildAgent: %v", err)
	}
	ag := built.agent
	if ag.Workspace == nil || !ag.Workspace.Admission().Explicit() {
		t.Fatal("the agent does not see the workspace's explicit mode")
	}
	for _, name := range []string{"write_file", "edit_file"} {
		if _, ok := ag.Tools.Get(name); !ok {
			t.Errorf("%s is not registered", name)
		}
	}
}

func TestAdmissionFlagsReachBackgroundAndHelp(t *testing.T) {
	_, o := newFlagSet()
	o.fileMode, o.admit, o.attach = "explicit", stringList{"a.go"}, stringList{"b.md"}
	joined := strings.Join(bgRunArgs(o), " ")
	for _, want := range []string{"--file-mode=explicit", "--admit=a.go", "--attach=b.md"} {
		if !strings.Contains(joined, want) {
			t.Errorf("--bg child args lack %q: %s", want, joined)
		}
	}
	fs, _ := newFlagSet()
	for _, name := range []string{"file-mode", "admit", "attach"} {
		if fs.Lookup(name) == nil {
			t.Errorf("flag --%s is not defined", name)
		}
	}
}
