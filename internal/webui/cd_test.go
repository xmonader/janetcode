package webui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

func cdServer(t *testing.T) (s *server, cd *agent.CD, here, sib string) {
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
	ag := &agent.Agent{Tools: tool.NewRegistry(), Model: "m", Log: sess, Root: here}
	ws := tool.NewLocal(here)
	cd = agent.NewCD(here)
	cd.Bind(ws, ag, sess)
	s = newServer(context.Background(), Options{Agent: ag, Session: sess, Provider: "fake", Cwd: here, Workspace: ws, CD: cd})
	return s, cd, here, sib
}

func TestWebCdMovesTheSession(t *testing.T) {
	s, cd, here, sib := cdServer(t)
	s.cd("../sibling")
	if got := cd.Dir(); got != sib {
		t.Errorf("cd.Dir() = %q, want %q", got, sib)
	}
	// The server's own idea of where it is has to follow: it roots path
	// completion, uploads and the "different directory" guard below.
	if got := s.cwd(); got != sib {
		t.Errorf("server cwd = %q, want %q", got, sib)
	}
	if got := s.opts.Workspace.Root(); got != sib {
		t.Errorf("workspace root = %q, want %q", got, sib)
	}
	notices := s.noticeTexts()
	if len(notices) == 0 || !strings.Contains(notices[len(notices)-1], sib) {
		t.Errorf("notices = %q, want the last one to name %s", notices, sib)
	}
	if strings.Contains(strings.Join(notices, "\n"), here+"\n") {
		t.Error("the move reported the directory it left as the new root")
	}
}

func TestWebCdSessionGuardFollowsTheMove(t *testing.T) {
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	sessDir, err := session.Dir()
	if err != nil {
		t.Fatal(err)
	}
	s, _, here, sib := cdServer(t)
	// One session recorded in each directory. The guard lets you load the
	// one that belongs where the server is, and refuses the other.
	mk := func(cwd string) string {
		sess, err := session.New(sessDir, cwd, "fake", "m")
		if err != nil {
			t.Fatal(err)
		}
		if err := sess.EnsureWritten(); err != nil {
			t.Fatal(err)
		}
		return sess.Meta.ID
	}
	inHere, inSib := mk(here), mk(sib)

	load := func(id string) int {
		body := strings.NewReader(`{"id":"` + id + `"}`)
		req := httptest.NewRequest(http.MethodPost, "/api/load", body)
		rec := httptest.NewRecorder()
		s.loadSession(rec, req)
		return rec.Code
	}
	s.cd(sib)
	// The guard compares against where the server is now, so the session
	// recorded in the directory it moved to is the one it will load.
	if code := load(inSib); code != http.StatusOK {
		t.Errorf("after /cd, loading the new directory's session = %d, want 200", code)
	}
	if code := load(inHere); code != http.StatusBadRequest {
		t.Errorf("after /cd, loading the old directory's session = %d, want 400", code)
	}
}

func TestWebCdRefusalAndReport(t *testing.T) {
	s, cd, here, _ := cdServer(t)
	s.cd("nowhere")
	if cd.Dir() != here {
		t.Error("a refused /cd moved the session")
	}
	if s.cwd() != here {
		t.Error("a refused /cd moved the server's working directory")
	}
	s.cd("")
	sheet := s.sheetText(t)
	for _, want := range []string{here, "/ssh", "/docker"} {
		if !strings.Contains(sheet, want) {
			t.Errorf("bare /cd sheet does not mention %q:\n%s", want, sheet)
		}
	}
}

func TestWebCdWithoutATrackerSaysSo(t *testing.T) {
	s := newServer(context.Background(), Options{Cwd: t.TempDir()})
	s.cd("/tmp")
	if len(s.errorTexts()) == 0 {
		t.Fatal("a session with no tracker should report that /cd is unavailable")
	}
}

// errorTexts is every error entry the server rendered.
func (s *server) errorTexts() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, e := range s.entries {
		if e.Kind == "error" {
			out = append(out, e.Text)
		}
	}
	return out
}
