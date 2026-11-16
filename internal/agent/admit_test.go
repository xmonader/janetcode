package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/admit"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/snapshot"
	"github.com/xmonader/janetcode/internal/tool"
)

// admitAgent is an agent in a committed repository with file tools and
// bash all on one workspace, in explicit file mode with nothing admitted
// but the files named.
func admitAgent(t *testing.T, f *fake, files map[string]string, admitted ...string) (*Agent, *tool.Workspace, string) {
	t.Helper()
	root := t.TempDir()
	for p, c := range files {
		os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755)
		if err := os.WriteFile(filepath.Join(root, p), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitInit(t, root)
	ws := tool.NewLocal(root)
	ws.Admission().SetMode(admit.Explicit)
	for _, p := range admitted {
		if _, err := ws.Admit(p, false); err != nil {
			t.Fatal(err)
		}
	}
	repo, err := snapshot.Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	a := &Agent{
		Provider: f, Model: "m", System: "sys", AutoApprove: true,
		Tools: tool.NewRegistry(
			tool.ReadFile{Root: root, WS: ws}, tool.WriteFile{Root: root, WS: ws},
			tool.EditFile{Root: root, WS: ws}, tool.Bash{Root: root, WS: ws},
		),
		Workspace: ws, Root: root,
	}
	a.Snapshots = repo
	return a, ws, root
}

// toolResults is every tool result in the conversation, in order.
func toolResults(a *Agent) []string {
	var out []string
	for _, m := range a.Messages {
		for _, b := range m.Blocks {
			if b.Type == llm.BlockToolResult {
				out = append(out, b.Text)
			}
		}
	}
	return out
}

func TestExplicitModeEditAndWriteAreRefusedThroughTheAgent(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("c1", "write_file", `{"path":"b.txt","content":"changed"}`),
		toolCall("c2", "edit_file", `{"path":"a.txt","old_string":"alpha","new_string":"ALPHA"}`),
		toolCall("c3", "write_file", `{"path":"new.txt","content":"x"}`),
		text("done"),
	}}
	a, _, root := admitAgent(t, f, map[string]string{"a.txt": "alpha\n", "b.txt": "beta\n"}, "a.txt")
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	res := toolResults(a)
	if len(res) != 3 {
		t.Fatalf("results = %q", res)
	}
	for i, want := range []string{"/admit b.txt", "", "/admit new.txt"} {
		if want == "" {
			if strings.Contains(res[i], "not admitted") {
				t.Errorf("admitted file refused: %q", res[i])
			}
			continue
		}
		if !strings.Contains(res[i], want) || !strings.Contains(res[i], "not admitted") {
			t.Errorf("result %d = %q, want a refusal naming %q", i, res[i], want)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(root, "b.txt")); string(b) != "beta\n" {
		t.Errorf("b.txt = %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "a.txt")); !strings.Contains(string(b), "ALPHA") {
		t.Errorf("admitted a.txt not edited: %q", b)
	}
	if _, err := os.Stat(filepath.Join(root, "new.txt")); err == nil {
		t.Error("a refused write created new.txt")
	}
}

// A shell command whose write targets show in its text is refused before
// it runs, whatever the permission mode (the agent is on auto-approve).
func TestExplicitModeBashWritesAreRefusedBeforeRunning(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("c1", "bash", `{"command":"echo hi > made.txt"}`),
		toolCall("c2", "bash", `{"command":"echo ok > allowed.txt; cp allowed.txt copy.txt"}`),
		toolCall("c3", "bash", `{"command":"echo ok > allowed.txt"}`),
		text("done"),
	}}
	a, _, root := admitAgent(t, f, map[string]string{"a.txt": "alpha\n"}, "allowed.txt")
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	res := toolResults(a)
	if !strings.Contains(res[0], "/admit made.txt") {
		t.Errorf("redirect result = %q", res[0])
	}
	if !strings.Contains(res[1], "/admit copy.txt") {
		t.Errorf("a command with one admitted and one not result = %q", res[1])
	}
	for _, name := range []string{"made.txt", "copy.txt"} {
		if _, err := os.Stat(filepath.Join(root, name)); err == nil {
			t.Errorf("%s was created by a refused command", name)
		}
	}
	// A refused compound command did not run at all, not even its
	// admitted half.
	if b, err := os.ReadFile(filepath.Join(root, "allowed.txt")); err != nil || string(b) != "ok\n" {
		t.Errorf("allowed.txt = %q, %v (the third command writes it)", b, err)
	}
	if strings.Contains(res[2], "not admitted") {
		t.Errorf("a write to an admitted file was refused: %q", res[2])
	}
}

// A command whose writes are not in its text (a script) is watched: what
// it changed that is not admitted is put back, and the model is told.
func TestExplicitModeOpaqueBashChangesArePutBack(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("c1", "bash", `{"command":"sh gen.sh"}`),
		text("done"),
	}}
	files := map[string]string{
		"gen.sh":       "echo made > made.txt\necho changed > tracked.txt\necho gen > out/gen.txt\nmkdir -p out\necho again > admitted.txt\n",
		"tracked.txt":  "original\n",
		"admitted.txt": "first\n",
	}
	a, _, root := admitAgent(t, f, files, "admitted.txt", "out/")
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "made.txt")); err == nil {
		t.Error("a file the script created outside the admitted set was left behind")
	}
	if b, _ := os.ReadFile(filepath.Join(root, "tracked.txt")); string(b) != "original\n" {
		t.Errorf("tracked.txt = %q, want the original put back", b)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "admitted.txt")); string(b) != "again\n" {
		t.Errorf("admitted.txt = %q, want the script's change kept", b)
	}
	res := toolResults(a)[0]
	for _, want := range []string{"file mode explicit", "made.txt", "tracked.txt", "/admit"} {
		if !strings.Contains(res, want) {
			t.Errorf("result lacks %q:\n%s", want, res)
		}
	}
	if strings.Contains(res, "admitted.txt,") || strings.Contains(res, "Put back: admitted") {
		t.Errorf("the note blames an admitted file:\n%s", res)
	}
}

// A file that already held uncommitted edits cannot be restored from the
// index, so it is named instead of silently lost or silently kept.
func TestExplicitModeOpaqueChangeToADirtyFileIsReported(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("c1", "bash", `{"command":"sh gen.sh"}`),
		text("done"),
	}}
	a, _, root := admitAgent(t, f, map[string]string{"gen.sh": "echo script >> dirty.txt\n", "dirty.txt": "committed\n"})
	os.WriteFile(filepath.Join(root, "dirty.txt"), []byte("user edit\n"), 0o644)
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	res := toolResults(a)[0]
	if !strings.Contains(res, "Could not put back") || !strings.Contains(res, "dirty.txt") {
		t.Errorf("result does not name the file it could not restore:\n%s", res)
	}
}

func TestWorkspaceModeLeavesBashAlone(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("c1", "bash", `{"command":"echo hi > made.txt; sh gen.sh"}`),
		text("done"),
	}}
	a, ws, root := admitAgent(t, f, map[string]string{"gen.sh": "echo g > g.txt\n"})
	ws.Admission().SetMode(admit.Workspace)
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"made.txt", "g.txt"} {
		if _, err := os.Stat(filepath.Join(root, n)); err != nil {
			t.Errorf("workspace mode: %s not created: %v", n, err)
		}
	}
	if strings.Contains(toolResults(a)[0], "file mode explicit") {
		t.Errorf("workspace mode produced an admission note: %q", toolResults(a)[0])
	}
}

// The model is told what it may edit, and what is only context.
func TestSystemPromptListsAdmittedFiles(t *testing.T) {
	f := &fake{responses: []*llm.Response{text("ok")}}
	a, ws, _ := admitAgent(t, f, map[string]string{"a.txt": "a\n", "README.md": "r\n"}, "a.txt")
	if _, err := ws.Admit("README.md", true); err != nil {
		t.Fatal(err)
	}
	if err := a.Run(context.Background(), "hi", nil); err != nil {
		t.Fatal(err)
	}
	sys := f.requests[0].System
	for _, want := range []string{"# File admission (explicit)", "- a.txt", "Attached read-only", "- README.md"} {
		if !strings.Contains(sys, want) {
			t.Errorf("system prompt lacks %q:\n%s", want, sys)
		}
	}
	ws.Admission().SetMode(admit.Workspace)
	if strings.Contains(a.systemPrompt(), "File admission") {
		t.Error("workspace mode still lists admission in the prompt")
	}
}

func TestAdmitCommandGrammarAndPersistence(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "src"), 0o755)
	os.WriteFile(filepath.Join(root, "src/a.go"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(root, "README.md"), []byte("x"), 0o644)
	ws := tool.NewLocal(root)
	sess, err := session.New(t.TempDir(), root, "p", "m")
	if err != nil {
		t.Fatal(err)
	}

	out, err := Admit(ws, sess, "")
	if err != nil || !strings.Contains(out, "file mode: workspace") {
		t.Fatalf("status = %q, %v", out, err)
	}
	if out, err = Admit(ws, sess, "mode explicit"); err != nil || !strings.Contains(out, "file mode: explicit") || !strings.Contains(out, "nothing is admitted yet") {
		t.Fatalf("mode explicit = %q, %v", out, err)
	}
	if out, err = Admit(ws, sess, "src"); err != nil || !strings.Contains(out, "src/ (1 files: a.go)") {
		t.Fatalf("admit dir = %q, %v", out, err)
	}
	if out, err = Attach(ws, sess, "README.md"); err != nil || !strings.Contains(out, "attached read-only") {
		t.Fatalf("attach = %q, %v", out, err)
	}
	if sess.Meta.Admission == nil || sess.Meta.Admission.Mode != admit.Explicit ||
		len(sess.Meta.Admission.Edit) != 1 || sess.Meta.Admission.Edit[0] != "src/" ||
		len(sess.Meta.Admission.Attach) != 1 {
		t.Fatalf("persisted = %+v", sess.Meta.Admission)
	}
	// Errors are reported, not swallowed, and change nothing.
	for _, bad := range []string{"mode strict", "mode", "remove nothing.go", "../outside.txt"} {
		if _, err := Admit(ws, sess, bad); err == nil {
			t.Errorf("/admit %s: no error", bad)
		}
	}
	if out, err = Admit(ws, sess, "remove src"); err != nil || strings.Contains(out, "src/") && strings.Contains(out, "admitted for editing") {
		t.Fatalf("remove = %q, %v", out, err)
	}
	if _, err = Admit(ws, sess, "clear"); err != nil {
		t.Fatal(err)
	}
	if e, a := ws.Admission().Lines(); len(e)+len(a) != 0 {
		t.Errorf("lists after clear: %v %v", e, a)
	}
	if _, err := Admit(ws, sess, "mode workspace"); err != nil {
		t.Fatal(err)
	}
	if sess.Meta.Admission != nil {
		t.Errorf("default policy still recorded: %+v", sess.Meta.Admission)
	}
	if _, err := Admit(nil, nil, "x"); err == nil {
		t.Error("no workspace: no error")
	}
}

// A prompt's @mention is context sent to the model; in explicit mode it
// joins the read-only list, and never the editable one.
func TestMentionsBecomeReadOnlyContext(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "ctx.md"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(root, "edit.go"), []byte("x"), 0o644)
	ws := tool.NewLocal(root)
	NoteMentions(ws, nil, []string{"ctx.md"})
	if e, a := ws.Admission().Lines(); len(e)+len(a) != 0 {
		t.Fatalf("workspace mode recorded mentions: %v %v", e, a)
	}
	ws.Admission().SetMode(admit.Explicit)
	ws.Admit("edit.go", false)
	NoteMentions(ws, nil, []string{"ctx.md", "edit.go", "missing/../../nope"})
	e, a := ws.Admission().Lines()
	if len(e) != 1 || e[0] != "edit.go" || len(a) != 1 || a[0] != "ctx.md" {
		t.Fatalf("edit=%v attach=%v", e, a)
	}
}

// An editing subagent works in a scratch checkout; what it changes
// reaches the project only through merge-back, which refuses a file that
// is not admitted.
func TestMergeBackRefusesFilesThatAreNotAdmitted(t *testing.T) {
	prov := &occScript{}
	prov.step = func(n int, repair bool) *llm.Response {
		switch n {
		case 0:
			return editCall("c1", "a.go", `return "a"`, `return "A"`)
		}
		return occSay("REPORT: A now says A")
	}
	root, parent, task := occSetup(t, map[string]string{"a.go": occGo, "keep.txt": "k\n"}, prov)
	ws := tool.NewLocal(root)
	ws.Admission().SetMode(admit.Explicit)
	ws.Admit("keep.txt", false)
	parent.Workspace = ws

	out, err := task.Run(context.Background(), json.RawMessage(`{"description":"change A","prompt":"change A","edit":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(root, "a.go")); got != occGo {
		t.Fatalf("a non-admitted file was changed by merge-back:\n%s", got)
	}
	if !strings.Contains(out, "file mode is explicit") || !strings.Contains(out, "/admit a.go") {
		t.Fatalf("the report does not say why the merge was refused:\n%s", out)
	}

	// Admitted, the same change merges.
	ws.Admit("a.go", false)
	prov2 := &occScript{step: prov.step}
	parent.Provider = prov2
	out, err = task.Run(context.Background(), json.RawMessage(`{"description":"change A again","prompt":"change A","edit":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(root, "a.go")); !strings.Contains(got, `return "A"`) {
		t.Fatalf("an admitted file was not merged:\n%s\n%s", got, out)
	}
}

// A fork writes into the session's own tree, so the session's admission
// governs its shell calls too.
func TestForkInheritsTheWorkspaceAdmission(t *testing.T) {
	f := &fake{responses: []*llm.Response{text("x")}}
	a, ws, root := admitAgent(t, f, map[string]string{"a.txt": "a\n"})
	task := &Task{Parent: a, Root: root}
	a.Tools.Add(task)
	child, err := task.forkChild()
	if err != nil {
		t.Fatal(err)
	}
	if child.Workspace != ws {
		t.Fatal("a fork lost the session's workspace, and with it the admission policy")
	}
	bash, _ := child.Tools.Get("bash")
	in := json.RawMessage(`{"command":"echo x > fork.txt"}`)
	if reason, denied := child.toolGate(bash, in); !denied || !strings.Contains(reason, "/admit fork.txt") {
		t.Fatalf("fork gate = %q, %v", reason, denied)
	}
}
