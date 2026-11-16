package tool

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/xmonader/janetcode/internal/shell"
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

// fakeSSHScript emulates both sides of an ssh exec the way the real
// protocol works: stage 1 is the ssh client, joining every argument
// after the target into ONE command string (the SSH exec protocol
// carries a single string); stage 2 is the remote login shell, parsing
// that string. eval honours the quoting the client applied, so a word
// the client left unquoted becomes shell syntax here — which is the
// injection remoteWords guards against.
const fakeSSHScript = `#!/bin/sh
if [ "$FAKE_STAGE" != "remote" ]; then
	[ -n "$FAKE_ARGLOG" ] && printf '%s\n' "$@" >> "$FAKE_ARGLOG"
	while [ $# -gt 0 ] && [ "$1" != "--" ]; do shift; done
	[ "$1" = "--" ] && shift
	joined=""
	for w in "$@"; do joined="$joined $w"; done
	FAKE_STAGE=remote FAKE_FS="$FAKE_FS" exec sh -c '"$0" "$@"' "$0" "$joined"
fi
eval "set -- $1"
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
find) shift; find "$@" ;;
chmod) exit 0 ;;
sh|bash) shift; { [ "$1" = "-lc" ] || [ "$1" = "-c" ]; } && shift; eval "$1" ;;
*) echo "unexpected: $*" >&2; exit 1 ;;
esac
`

// installFakeSSH puts the two-stage fake ssh on PATH and returns the
// directory it roots at.
func installFakeSSH(t *testing.T) string {
	t.Helper()
	fs := t.TempDir()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(fakeSSHScript), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_FS", fs)
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Fatal(err)
	}
	return fs
}

func TestDialSSHWithFakeBinary(t *testing.T) {
	fs := installFakeSSH(t)
	os.WriteFile(filepath.Join(fs, "note.txt"), []byte("remote-hi\n"), 0o644)
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

// A remote walk honours SkipDir the way filepath.WalkDir does: nothing
// under a skipped directory is visited. It used to skip only the
// directory's own entry, so a glob over ssh listed .git and node_modules.
func TestRemoteWalkDirSkipsWhatIsUnderASkippedDirectory(t *testing.T) {
	fs := installFakeSSH(t)
	for _, p := range []string{"src/a.go", ".git/objects/x", "node_modules/m/i.js", "b.txt"} {
		full := filepath.Join(fs, p)
		os.MkdirAll(filepath.Dir(full), 0o755)
		os.WriteFile(full, []byte("x"), 0o644)
	}
	h, err := DialSSH("user@host")
	if err != nil {
		t.Fatal(err)
	}
	ws := NewLocal(t.TempDir())
	ws.Set(h)
	var got []string
	err = walkOn(ws, context.Background(), fs, fs, func(rel string, d os.DirEntry) error {
		if d.IsDir() {
			rel += "/"
		}
		got = append(got, rel)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(got, " ")
	for _, want := range []string{"src/", "src/a.go", "b.txt"} {
		if !strings.Contains(" "+joined+" ", " "+want+" ") {
			t.Fatalf("missing %s in %q", want, joined)
		}
	}
	if strings.Contains(joined, ".git") || strings.Contains(joined, "node_modules") {
		t.Fatalf("walked into a skipped directory: %q", joined)
	}
}

// TestSSHRemoteWordsShellOpaque is the injection fix: the SSH exec
// protocol carries one command string that the remote shell parses, so a
// model-controlled path handed to a read-only, never-prompting tool must
// arrive as one opaque word. Without quoting, "note.txt; cmd" reparses
// into a second command on the remote host.
func TestSSHRemoteWordsShellOpaque(t *testing.T) {
	fs := installFakeSSH(t)
	os.WriteFile(filepath.Join(fs, "note.txt"), []byte("remote-hi\n"), 0o644)
	h, err := DialSSH("user@host")
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(fs, "INJECTED")
	hostile := "note.txt; echo hi > " + marker
	if _, err := h.ReadFile(context.Background(), filepath.Join(fs, hostile)); err == nil {
		t.Fatal("read of a nonexistent file succeeded")
	}
	if _, serr := os.Stat(marker); serr == nil {
		t.Fatal("the remote shell executed a command smuggled inside a read_file path")
	}
	// Words that are awkward but honest round-trip intact.
	spaced := "note file.txt"
	if err := os.WriteFile(filepath.Join(fs, spaced), []byte("spaced\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := h.ReadFile(context.Background(), filepath.Join(fs, spaced))
	if err != nil || string(got) != "spaced\n" {
		t.Fatalf("read of a spaced name: %q err=%v", got, err)
	}
	// The bash path joins its script into the same string; quoting keeps
	// the whole script one word, so a multi-word command survives.
	cmd, err := h.Command(context.Background(), "echo one two")
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "one two" {
		t.Fatalf("bash through ssh: %q err=%v", out, err)
	}
}

// A secret the user gave at a prompt reaches a command on an ssh
// workspace, but not through anything a process list or a log shows: the
// values go over ssh's stdin into a private file the command sources and
// removes, so the ssh argument vector carries only the file's name.
func TestSSHCommandReceivesSessionSecrets(t *testing.T) {
	fs := installFakeSSH(t)
	argLog := filepath.Join(t.TempDir(), "args")
	t.Setenv("FAKE_ARGLOG", argLog)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	const value = "p@ss'w\"o rd$(echo x > pwned)\nline2"
	shell.ClearSessionSecrets()
	t.Cleanup(shell.ClearSessionSecrets)
	shell.SetSessionSecret("JANETCODE_SECRET_TOK", value)
	h, err := DialSSH("user@host")
	if err != nil {
		t.Fatal(err)
	}
	cmd, err := h.Command(context.Background(), `printf %s "$JANETCODE_SECRET_TOK"`)
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.CombinedOutput()
	if err != nil || string(out) != value {
		t.Fatalf("remote saw %q err=%v, want %q", out, err, value)
	}
	args := readTestFile(t, argLog)
	if strings.Contains(args, "p@ss") || strings.Contains(args, "line2") {
		t.Fatalf("a secret value is on an ssh argument line:\n%s", args)
	}
	if left, _ := filepath.Glob(filepath.Join(tmp, "*")); len(left) != 0 {
		t.Fatalf("the staged secret file was not removed: %v", left)
	}
	if _, err := os.Stat(filepath.Join(fs, "pwned")); err == nil {
		t.Fatal("the secret value was executed as shell on the remote")
	}

	// With no secret set there is no staging call, just the one command.
	shell.ClearSessionSecrets()
	os.Remove(argLog)
	cmd, _ = h.Command(context.Background(), "echo plain")
	if out, err := cmd.CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != "plain" {
		t.Fatalf("plain command: %q err=%v", out, err)
	}
	if strings.Contains(readTestFile(t, argLog), "mktemp") {
		t.Fatal("staged a secret file with no secrets set")
	}
}

// A docker workspace takes the secret by name: docker copies the value
// from its own environment, so it is not on the command line either.
func TestDockerCommandReceivesSessionSecretsByName(t *testing.T) {
	bin := t.TempDir()
	argLog := filepath.Join(t.TempDir(), "args")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$FAKE_ARGLOG\"\nprintf '%s' \"$JANETCODE_SECRET_TOK\" > \"$FAKE_ARGLOG.env\"\n"
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_ARGLOG", argLog)
	shell.ClearSessionSecrets()
	t.Cleanup(shell.ClearSessionSecrets)
	shell.SetSessionSecret("JANETCODE_SECRET_TOK", "s3cr3t-value")
	h := &remoteHost{kind: "docker", target: "box", root: "/work"}
	cmd, err := h.Command(context.Background(), "true")
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	args := readTestFile(t, argLog)
	if !strings.Contains(args, "-e\nJANETCODE_SECRET_TOK\n") || strings.Contains(args, "s3cr3t-value") {
		t.Fatalf("docker args = %q", args)
	}
	if got := readTestFile(t, argLog+".env"); got != "s3cr3t-value" {
		t.Fatalf("docker's environment held %q", got)
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(b)
}
