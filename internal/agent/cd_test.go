package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/plan"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

// cdFixture is a session rooted at one of two sibling directories, with
// the three things agent.CD moves itself bound to it.
func cdFixture(t *testing.T) (cd *CD, ws *tool.Workspace, ag *Agent, sess *session.Session, here, sib string) {
	t.Helper()
	base := t.TempDir()
	here, sib = filepath.Join(base, "here"), filepath.Join(base, "sibling")
	for _, d := range []string{here, sib} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	sess, err := session.New(t.TempDir(), here, "fake", "m")
	if err != nil {
		t.Fatal(err)
	}
	ws = tool.NewLocal(here)
	ag = &Agent{Root: here, System: "sys\n\n# Environment\n- Working directory: " + here + "\n"}
	cd = NewCD(here)
	cd.Bind(ws, ag, sess)
	return cd, ws, ag, sess, here, sib
}

func TestChangeDirMovesTheSessionToASibling(t *testing.T) {
	cd, ws, ag, sess, here, sib := cdFixture(t)

	status, err := cd.Change("../sibling")
	if err != nil {
		t.Fatalf("/cd ../sibling: %v", err)
	}
	if !strings.Contains(status, sib) {
		t.Errorf("status = %q, want it to name %s", status, sib)
	}
	// The file tools and bash run through the workspace, so moving it is
	// what moves them.
	if got := ws.Root(); got != sib {
		t.Errorf("workspace root = %q, want %q", got, sib)
	}
	if !ws.Local() {
		t.Error("a /cd must leave the workspace local")
	}
	if got := cd.Dir(); got != sib {
		t.Errorf("cd.Dir() = %q, want %q", got, sib)
	}
	// The agent's own root: the plan file and the paths shown by /undo.
	if got := ag.RootDir(); got != sib {
		t.Errorf("agent root = %q, want %q", got, sib)
	}
	if got, want := plan.File(ag.planRoot()), plan.File(sib); got != want {
		t.Errorf("plan path = %q, want %q", got, want)
	}
	if strings.Contains(plan.File(ag.planRoot()), here) {
		t.Error("the plan file still points at the directory the session left")
	}
	// The metadata is what --continue reads back.
	if sess.Meta.Cwd != sib {
		t.Errorf("session cwd = %q, want %q", sess.Meta.Cwd, sib)
	}
}

func TestChangeDirMovesTheFileTools(t *testing.T) {
	cd, ws, _, _, here, sib := cdFixture(t)
	for dir, body := range map[string]string{here: "old\n", sib: "new\n"} {
		if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	read := tool.ReadFile{Root: here, WS: ws}
	out, err := read.Run(context.Background(), json.RawMessage(`{"path":"note.txt"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "old") {
		t.Fatalf("before the move, a relative read got %q", out)
	}
	if _, err := cd.Change(sib); err != nil {
		t.Fatal(err)
	}
	// The tool was built with the old Root; it follows because it shares
	// the workspace pointer, exactly as it does for /ssh.
	out, err = read.Run(context.Background(), json.RawMessage(`{"path":"note.txt"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "new") || strings.Contains(out, "old") {
		t.Errorf("after /cd, a relative read got %q; it should read the new directory's file", out)
	}
}

func TestChangeDirMovesEveryRegisteredConsumer(t *testing.T) {
	cd, _, _, _, here, sib := cdFixture(t)
	// A stand-in for each thing the composition root registers: it records
	// the directory it was moved to, so the table below can assert that
	// none of them was left pointing at the old root.
	moved := map[string]string{}
	for _, name := range []string{
		"system prompt", "repository map", "memory", "skills and rules",
		"subagents", "execute_code", "language servers", "snapshots",
		"archive", "running commands", "project hooks", "verification",
		"permissions", "mcp servers", "composition root",
	} {
		cd.Add(name, func(dir string) (string, error) {
			moved[name] = dir
			return "", nil
		})
	}
	if _, err := cd.Change(sib); err != nil {
		t.Fatal(err)
	}
	for _, name := range cd.Consumers() {
		switch got := moved[name]; {
		case got == "":
			t.Errorf("%s was never told the session moved", name)
		case got == here:
			t.Errorf("%s still points at the old root", name)
		case got != sib:
			t.Errorf("%s moved to %q, want %q", name, got, sib)
		}
	}
	if len(cd.Consumers()) != len(moved) {
		t.Errorf("Consumers() lists %d, %d moved", len(cd.Consumers()), len(moved))
	}
}

func TestChangeDirTellsTheModelOnce(t *testing.T) {
	cd, _, ag, _, here, sib := cdFixture(t)
	if _, err := cd.Change(sib); err != nil {
		t.Fatal(err)
	}
	notes := cdNotes(ag)
	if len(notes) != 1 {
		t.Fatalf("got %d harness note(s), want exactly 1: %q", len(notes), notes)
	}
	for _, want := range []string{"/cd", here, sib} {
		if !strings.Contains(notes[0], want) {
			t.Errorf("harness note does not mention %q: %s", want, notes[0])
		}
	}
	// A refused move says nothing to the model at all.
	if _, err := cd.Change(filepath.Join(sib, "nope")); err == nil {
		t.Fatal("a missing directory should be refused")
	}
	if n := len(cdNotes(ag)); n != 1 {
		t.Errorf("a refused /cd added a note: %d notes", n)
	}
}

func TestChangeDirRefusals(t *testing.T) {
	cd, ws, ag, sess, here, _ := cdFixture(t)
	file := filepath.Join(here, "a.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ arg, want string }{
		{"nope", "no such directory"},
		{"a.txt", "is not a directory"},
	} {
		if _, err := cd.Change(c.arg); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("/cd %s = %v, want it to mention %q", c.arg, err, c.want)
		}
	}
	// Nothing moved on a refusal.
	if ws.Root() != here || ag.RootDir() != here || cd.Dir() != here || sess.Meta.Cwd != here {
		t.Error("a refused /cd moved something")
	}
}

func TestChangeDirRefusesWhileTheWorkspaceIsRemote(t *testing.T) {
	cd, ws, _, _, here, sib := cdFixture(t)
	ws.Set(fakeRemote{root: "/srv/app"})
	_, err := cd.Change(sib)
	if err == nil {
		t.Fatal("/cd on a remote workspace should be refused")
	}
	if !strings.Contains(err.Error(), "/ssh off") {
		t.Errorf("refusal should point at /ssh off: %v", err)
	}
	if cd.Dir() != here {
		t.Error("a refused /cd moved the session")
	}
}

func TestChangeDirIntoAnExtraDirIsFine(t *testing.T) {
	cd, ws, _, _, _, sib := cdFixture(t)
	if err := ws.AddDir(sib); err != nil {
		t.Fatal(err)
	}
	if _, err := cd.Change(sib); err != nil {
		t.Fatalf("/cd into an /add-dir directory: %v", err)
	}
	if ws.Root() != sib {
		t.Errorf("workspace root = %q, want %q", ws.Root(), sib)
	}
}

func TestChangeDirWithNoArgumentReports(t *testing.T) {
	cd, ws, _, _, here, sib := cdFixture(t)
	if err := ws.AddDir(sib); err != nil {
		t.Fatal(err)
	}
	for _, arg := range []string{"", "  ", "status"} {
		got, err := cd.Change(arg)
		if err != nil {
			t.Fatalf("/cd %q: %v", arg, err)
		}
		for _, want := range []string{here, sib, "/ssh", "/docker"} {
			if !strings.Contains(got, want) {
				t.Errorf("/cd %q report does not mention %q:\n%s", arg, want, got)
			}
		}
	}
	if cd.Dir() != here {
		t.Error("a bare /cd moved the session")
	}
}

func TestChangeDirToWhereItAlreadyIsSaysSo(t *testing.T) {
	cd, _, ag, _, here, _ := cdFixture(t)
	got, err := cd.Change(here)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "already") {
		t.Errorf("status = %q, want it to say the session is already there", got)
	}
	if len(cdNotes(ag)) != 0 {
		t.Error("a no-op /cd told the model something happened")
	}
}

func TestChangeDirReportsAConsumerThatCouldNotMove(t *testing.T) {
	cd, _, _, _, _, sib := cdFixture(t)
	cd.Add("language servers", func(string) (string, error) { return "restarted gopls", nil })
	cd.Add("running commands", func(string) (string, error) { return "", errNotMovable })
	got, err := cd.Change(sib)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"restarted gopls", "running commands", errNotMovable.Error()} {
		if !strings.Contains(got, want) {
			t.Errorf("status does not mention %q:\n%s", want, got)
		}
	}
}

func TestSystemPromptSetterRefreshesTheExtra(t *testing.T) {
	a := &Agent{System: "old"}
	calls := 0
	a.SystemExtra = func() string { calls++; return "" }
	_ = a.extra()
	a.SetSystem("new")
	_ = a.extra()
	if a.SystemPrompt() != "new" {
		t.Errorf("SystemPrompt() = %q", a.SystemPrompt())
	}
	if calls != 2 {
		t.Errorf("SystemExtra evaluated %d times, want it re-evaluated after SetSystem", calls)
	}
}

// errNotMovable stands in for a consumer that cannot follow the move.
var errNotMovable = errors.New("a background job is still running there")

// fakeRemote is a workspace host that is not the local filesystem. Only
// the three methods /cd looks at are implemented.
type fakeRemote struct {
	tool.Host
	root string
}

func (f fakeRemote) Kind() string  { return "ssh" }
func (f fakeRemote) Label() string { return "user@host:" + f.root }
func (f fakeRemote) Root() string  { return f.root }
func (fakeRemote) Close() error    { return nil }

// cdNotes is every harness note about a /cd in the conversation. The note
// goes in as a synthetic user block, the same path /add-dir and /undo
// apply use, so this is where it lands.
func cdNotes(a *Agent) []string {
	var out []string
	for _, m := range a.MessagesSnapshot() {
		for _, b := range m.Blocks {
			if b.Synthetic && strings.Contains(b.Text, "/cd") {
				out = append(out, b.Text)
			}
		}
	}
	return out
}

func TestChangeDirIsFollowedByAResume(t *testing.T) {
	cd, _, _, sess, here, sib := cdFixture(t)
	dir := filepath.Dir(sess.Path())
	if err := sess.EnsureWritten(); err != nil {
		t.Fatal(err)
	}
	if _, err := cd.Change(sib); err != nil {
		t.Fatal(err)
	}
	// --continue in the new directory finds the session; the directory it
	// left no longer lists it.
	id, err := session.Latest(dir, sib)
	if err != nil {
		t.Fatalf("latest session in %s: %v", sib, err)
	}
	if id != sess.Meta.ID {
		t.Errorf("latest in the new directory = %q, want %q", id, sess.Meta.ID)
	}
	infos, err := session.List(dir, here)
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range infos {
		if in.Meta.ID == sess.Meta.ID {
			t.Error("the session still lists under the directory it left")
		}
	}
	// And what a resume reads back off disk is the new directory.
	reopened, _, err := session.Open(dir, sess.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Meta.Cwd != sib {
		t.Errorf("resumed cwd = %q, want %q", reopened.Meta.Cwd, sib)
	}
}
