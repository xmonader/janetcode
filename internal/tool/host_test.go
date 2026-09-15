package tool

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseSSHSpec(t *testing.T) {
	for _, tc := range []struct {
		in, target, path string
		err              bool
	}{
		{"user@host", "user@host", "", false},
		{"user@host:/var/app", "user@host", "/var/app", false},
		{"host:~/src", "host", "~/src", false},
		{"user@host:22", "", "", true},
		{"", "", "", true},
	} {
		target, path, err := ParseSSHSpec(tc.in)
		if tc.err {
			if err == nil {
				t.Errorf("%q: want error", tc.in)
			}
			continue
		}
		if err != nil || target != tc.target || path != tc.path {
			t.Errorf("%q: target=%q path=%q err=%v", tc.in, target, path, err)
		}
	}
}

func TestParseDockerSpec(t *testing.T) {
	for _, tc := range []struct {
		in, ctr, path string
	}{
		{"myctr", "myctr", ""},
		{"myctr:/app", "myctr", "/app"},
		{"myctr /app", "myctr", "/app"},
		{"registry.example/img:tag", "registry.example/img:tag", ""},
	} {
		ctr, path, err := ParseDockerSpec(tc.in)
		if err != nil || ctr != tc.ctr || path != tc.path {
			t.Errorf("%q: ctr=%q path=%q err=%v", tc.in, ctr, path, err)
		}
	}
}

func TestLocalWorkspaceMatchesOS(t *testing.T) {
	dir := t.TempDir()
	ws := NewLocal(dir)
	p := filepath.Join(dir, "a.txt")
	if err := ws.Host().WriteFile(context.Background(), p, []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ws.Host().ReadFile(context.Background(), p)
	if err != nil || string(got) != "hi\n" {
		t.Fatalf("got %q err=%v", got, err)
	}
	rf := ReadFile{Root: dir, WS: ws}
	out, err := rf.Run(context.Background(), json.RawMessage(`{"path":"a.txt"}`))
	if err != nil || !strings.Contains(out, "hi") {
		t.Fatalf("read_file: %q err=%v", out, err)
	}
}

func TestWorkspaceRetargetsFileTools(t *testing.T) {
	a := t.TempDir()
	b := t.TempDir()
	os.WriteFile(filepath.Join(a, "x.txt"), []byte("from-a\n"), 0o644)
	os.WriteFile(filepath.Join(b, "x.txt"), []byte("from-b\n"), 0o644)
	ws := NewLocal(a)
	rf := ReadFile{Root: a, WS: ws}
	out, err := rf.Run(context.Background(), json.RawMessage(`{"path":"x.txt"}`))
	if err != nil || !strings.Contains(out, "from-a") {
		t.Fatalf("first: %q err=%v", out, err)
	}
	ws.Set(LocalHost(b))
	out, err = rf.Run(context.Background(), json.RawMessage(`{"path":"x.txt"}`))
	if err != nil || !strings.Contains(out, "from-b") {
		t.Fatalf("after Set: %q err=%v", out, err)
	}
	if strings.Contains(out, "from-a") {
		t.Fatal("still seeing the old tree")
	}
}

func TestRetargetRequiresAWorkspace(t *testing.T) {
	if _, err := Retarget(nil, "/cwd", "ssh", "", nil); err == nil {
		t.Fatal("want error with no workspace")
	}
}

func TestRetargetStatusAndLocal(t *testing.T) {
	ws := NewLocal("/start")
	status, err := Retarget(ws, "/start", "ssh", "status", nil)
	if err != nil || !strings.Contains(status, "/start") {
		t.Fatalf("status=%q err=%v", status, err)
	}
	status, err = Retarget(ws, "/back", "ssh", "off", nil)
	if err != nil || !strings.Contains(status, "/back") || ws.Kind() != "local" {
		t.Fatalf("off: status=%q kind=%s err=%v", status, ws.Kind(), err)
	}
}

func TestRetargetDialFailureIsReported(t *testing.T) {
	ws := NewLocal("/start")
	dial := func(string) (Host, error) { return nil, errors.New("boom") }
	if _, err := Retarget(ws, "/start", "docker", "myctr", dial); err == nil || !strings.Contains(err.Error(), "docker: boom") {
		t.Fatalf("err = %v", err)
	}
}

func TestDialSSHWithFakeBinary(t *testing.T) {
	fs := t.TempDir()
	os.WriteFile(filepath.Join(fs, "note.txt"), []byte("remote-hi\n"), 0o644)
	bin := t.TempDir()
	script := `#!/bin/sh
while [ $# -gt 0 ] && [ "$1" != "--" ]; do shift; done
[ "$1" = "--" ] && shift
case "$1" in
pwd) printf '%s\n' "$FAKE_FS" ;;
cat)
  [ "$2" = "--" ] && shift
  shift
  cat -- "$1"
  ;;
test)
  shift
  flag=$1; shift
  if [ "$flag" = "-d" ]; then [ -d "$1" ]; else [ -e "$1" ]; fi
  ;;
ls) shift; [ "$1" = "-1Ap" ] && shift; ls -1Ap "$1" ;;
mkdir) shift; mkdir -p "$2" ;;
tee) shift; cat > "$1" ;;
find) shift; find "$1" ;;
chmod) exit 0 ;;
*) echo "unexpected: $*" >&2; exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_FS", fs)
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Fatal(err)
	}
	h, err := DialSSH("user@host")
	if err != nil {
		t.Fatal(err)
	}
	if h.Kind() != "ssh" || h.Root() != fs {
		t.Fatalf("host kind=%s root=%s", h.Kind(), h.Root())
	}
	got, err := h.ReadFile(context.Background(), filepath.Join(fs, "note.txt"))
	if err != nil || string(got) != "remote-hi\n" {
		t.Fatalf("read: %q err=%v", got, err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := h.ReadFile(cancelled, filepath.Join(fs, "note.txt")); err == nil {
		t.Fatal("read with a cancelled context succeeded; the ssh helper ignored ctx")
	}
	ws := NewLocal(t.TempDir())
	ws.Set(h)
	rf := ReadFile{Root: "/unused", WS: ws}
	out, err := rf.Run(context.Background(), json.RawMessage(`{"path":"note.txt"}`))
	if err != nil || !strings.Contains(out, "remote-hi") {
		t.Fatalf("tool through ssh host: %q err=%v", out, err)
	}
}
