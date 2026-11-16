package webui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

func admitServer(t *testing.T) (*server, string) {
	t.Helper()
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "src"), 0o755)
	os.WriteFile(filepath.Join(root, "src", "a.go"), []byte("package src\n"), 0o644)
	os.WriteFile(filepath.Join(root, "README.md"), []byte("# r\n"), 0o644)
	sess, err := session.New(t.TempDir(), root, "fake", "m")
	if err != nil {
		t.Fatal(err)
	}
	ws := tool.NewLocal(root)
	ag := &agent.Agent{Tools: tool.NewRegistry(tool.WriteFile{Root: root, WS: ws}), Model: "m", Log: sess, Root: root, Workspace: ws}
	s := newServer(context.Background(), Options{Agent: ag, Session: sess, Provider: "fake", Cwd: root, Workspace: ws})
	return s, root
}

// The web UI shows the mode and the admitted paths in the status payload
// (the header chip reads it), prints the same report as the terminal, and
// keeps the session record in step.
func TestWebAdmitShowsModeAndPaths(t *testing.T) {
	s, _ := admitServer(t)
	if st := s.statusNow(); st.FileMode != "workspace" || len(st.Admitted) != 0 {
		t.Fatalf("default status = %+v", st)
	}
	s.admit("mode explicit", false)
	s.admit("src", false)
	s.admit("README.md", true)
	st := s.statusNow()
	if st.FileMode != "explicit" || len(st.Admitted) != 1 || st.Admitted[0] != "src/" || len(st.Attached) != 1 || st.Attached[0] != "README.md" {
		t.Fatalf("status = %+v", st)
	}
	s.admit("", false)
	text := s.sheetText(t)
	for _, want := range []string{"file mode: explicit", "src/ (1 files: a.go)", "attached read-only", "README.md"} {
		if !strings.Contains(text, want) {
			t.Errorf("report lacks %q:\n%s", want, text)
		}
	}
	if sess := s.sess(); sess.Meta.Admission == nil || sess.Meta.Admission.Mode != "explicit" || len(sess.Meta.Admission.Edit) != 1 {
		t.Errorf("session record = %+v", sess.Meta.Admission)
	}
	s.admit("../escape.txt", false)
	if got := s.noticeTexts(); strings.Contains(strings.Join(got, "\n"), "escape.txt") {
		t.Errorf("a path outside the workspace was admitted: %q", got)
	}
}

// The model's write goes through the same policy the page shows: the
// refusal text is what the model reads, and admitting from the page lets
// the same call through.
func TestWebAdmissionGovernsTheModelsWrites(t *testing.T) {
	s, root := admitServer(t)
	s.admit("mode explicit", false)
	wf, _ := s.opts.Agent.Tools.Get("write_file")
	in := []byte(`{"path":"README.md","content":"changed"}`)
	if _, err := wf.Run(context.Background(), in); err == nil || !strings.Contains(err.Error(), "/admit README.md") {
		t.Fatalf("write before admission: %v", err)
	}
	s.admit("README.md", false)
	if _, err := wf.Run(context.Background(), in); err != nil {
		t.Fatalf("write after /admit: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "README.md")); string(b) != "changed" {
		t.Errorf("README.md = %q", b)
	}
}

func TestWebAdmitCommandsAreRegistered(t *testing.T) {
	for _, c := range []string{"/admit", "/attach"} {
		if _, ok := webDispatch[c]; !ok {
			t.Errorf("%s has no web handler", c)
		}
	}
}
