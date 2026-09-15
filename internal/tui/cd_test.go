package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

func cdModel(t *testing.T) (m *model, cd *agent.CD, here, sib string) {
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
	ws := tool.NewLocal(here)
	ag := &agent.Agent{Tools: tool.NewRegistry(tool.WriteFile{Root: here, WS: ws}), Model: "m", Log: sess, Root: here}
	cd = agent.NewCD(here)
	cd.Bind(ws, ag, sess)
	m = newModel(Options{Agent: ag, Session: sess, Provider: "fake", Cwd: here, Version: "test", Workspace: ws, CD: cd})
	return m, cd, here, sib
}

func TestTuiCdMovesTheSessionAndTheUI(t *testing.T) {
	m, cd, here, sib := cdModel(t)
	m.cdCommand("../sibling")
	if cd.Dir() != sib {
		t.Errorf("cd.Dir() = %q, want %q", cd.Dir(), sib)
	}
	// Everything the terminal itself roots at the project: the directory
	// it completes paths from, shortens paths against, and starts new
	// sessions in.
	if m.opts.Cwd != sib {
		t.Errorf("opts.Cwd = %q, want %q", m.opts.Cwd, sib)
	}
	if m.transcript.cwd != sib {
		t.Errorf("transcript cwd = %q, want %q", m.transcript.cwd, sib)
	}
	if m.opts.Workspace.Root() != sib {
		t.Errorf("workspace root = %q, want %q", m.opts.Workspace.Root(), sib)
	}
	if !strings.Contains(m.status, sib) {
		t.Errorf("status = %q, want it to name %s", m.status, sib)
	}
	if strings.Contains(m.opts.Cwd, filepath.Base(here)) {
		t.Error("the terminal still points at the directory the session left")
	}
}

func TestTuiCdRefusalLeavesEverythingWhereItWas(t *testing.T) {
	m, cd, here, _ := cdModel(t)
	if err := os.WriteFile(filepath.Join(here, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, arg := range []string{"nowhere", "a.txt"} {
		m.cdCommand(arg)
		if cd.Dir() != here || m.opts.Cwd != here || m.opts.Workspace.Root() != here {
			t.Fatalf("/cd %s moved something it should not have", arg)
		}
	}
	if !strings.Contains(transcriptText(m), "no such directory") {
		t.Errorf("no refusal was shown:\n%s", transcriptText(m))
	}
	if !strings.Contains(transcriptText(m), "is not a directory") {
		t.Errorf("a file was not refused as a file:\n%s", transcriptText(m))
	}
}

func TestTuiCdWithNoArgumentReports(t *testing.T) {
	m, cd, here, sib := cdModel(t)
	if err := m.opts.Workspace.AddDir(sib); err != nil {
		t.Fatal(err)
	}
	m.cdCommand("")
	text := transcriptText(m)
	for _, want := range []string{here, sib, "/ssh", "/docker"} {
		if !strings.Contains(text, want) {
			t.Errorf("bare /cd does not mention %q:\n%s", want, text)
		}
	}
	if cd.Dir() != here {
		t.Error("a bare /cd moved the session")
	}
}

func TestTuiCdWithoutATrackerSaysSo(t *testing.T) {
	m, _, _, sib := cdModel(t)
	m.opts.CD = nil
	m.cdCommand(sib)
	if !strings.Contains(transcriptText(m), "no working directory") {
		t.Errorf("a session with no tracker should say so:\n%s", transcriptText(m))
	}
}

// transcriptText is everything the transcript has been told, joined.
func transcriptText(m *model) string {
	var b strings.Builder
	for _, e := range m.transcript.entries {
		b.WriteString(e.text)
		b.WriteString("\n")
	}
	return b.String()
}
