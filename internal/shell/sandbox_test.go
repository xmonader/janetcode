package shell

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSandboxOffByDefault(t *testing.T) {
	var s Sandbox
	if s.Enabled() {
		t.Fatal("sandbox must start off")
	}
	if Status := s.Status(); !strings.Contains(Status, "off") {
		t.Fatalf("status = %q", Status)
	}
	if (*Sandbox)(nil).Enabled() {
		t.Fatal("nil sandbox is on")
	}
}

func TestSandboxCommandDocker(t *testing.T) {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		t.Skip("docker wrap is the Linux path")
	}
	s := &Sandbox{look: func(name string) (string, error) { return "/usr/bin/" + name, nil }}
	if err := s.Enable(); err != nil {
		t.Fatal(err)
	}
	s.SetImage("alpine:test")
	cmd, err := s.Command(context.Background(), "/work", "echo hi")
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Join(cmd.Args, " ")
	for _, want := range []string{"run", "--rm", "-v /work:/work", "-w /work", "alpine:test", "echo hi"} {
		if !strings.Contains(args, want) {
			t.Errorf("args %q missing %q", args, want)
		}
	}
	s.Disable()
	if s.Enabled() {
		t.Fatal("still on")
	}
}

func TestSetSandboxNilReportsNoSandbox(t *testing.T) {
	if _, err := SetSandbox(nil, ""); err == nil {
		t.Fatal("want error for a nil sandbox")
	}
}

func TestSetSandboxOnOff(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sandbox is not supported on windows")
	}
	s := &Sandbox{look: func(name string) (string, error) { return "/usr/bin/" + name, nil }}
	status, err := SetSandbox(s, "on")
	if err != nil || !strings.Contains(status, "on") {
		t.Fatalf("status=%q err=%v", status, err)
	}
	status, err = SetSandbox(s, "off")
	if err != nil || !strings.Contains(status, "off") {
		t.Fatalf("status=%q err=%v", status, err)
	}
}

func TestSetSandboxUnknownArgIsAnImage(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sandbox is not supported on windows")
	}
	s := &Sandbox{look: func(name string) (string, error) { return "/usr/bin/" + name, nil }}
	status, err := SetSandbox(s, "myimage:latest")
	if err != nil || s.Image() != "myimage:latest" || !s.Enabled() {
		t.Fatalf("status=%q image=%q enabled=%v err=%v", status, s.Image(), s.Enabled(), err)
	}
}

// stubSandbox is a sandbox whose backend probes are answered by the
// test: docker and sandbox-exec are "installed", and the kernel reports
// the given Landlock ABI (0 for none).
func stubSandbox(t *testing.T, backend string, abi int) *Sandbox {
	t.Helper()
	s := &Sandbox{
		look: func(name string) (string, error) { return "/usr/bin/" + name, nil },
		abi:  func() int { return abi },
	}
	if backend != "" {
		if err := s.SetBackend(backend); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

// TestDockerArgvPerProfile pins the container each profile asks for:
// which roots are mounted read-write, which read-only, whether there is
// a network, and the read-only container a profile with no write roots
// gets.
func TestDockerArgvPerProfile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no sandbox on windows")
	}
	root := t.TempDir()
	extra := t.TempDir()
	gitDir := filepath.Join(root, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		profile string
		want    []string
		absent  []string
	}{
		{"workspace", []string{"--network bridge", "-v " + root + ":" + root + " ", "-v " + extra + ":" + extra + " ", "--tmpfs /tmp", "-w " + root}, []string{"--read-only", "--network none", gitDir + ":ro"}},
		{"read-only", []string{"--network bridge", "--read-only", "-v " + root + ":" + root + ":ro", "-v " + extra + ":" + extra + ":ro"}, []string{"--tmpfs"}},
		{"strict", []string{"--network none", "-v " + root + ":" + root + " ", "-v " + gitDir + ":" + gitDir + ":ro"}, []string{"--network bridge", "--read-only"}},
	}
	for _, c := range cases {
		t.Run(c.profile, func(t *testing.T) {
			s := stubSandbox(t, BackendDocker, 0)
			if _, err := SetSandbox(s, c.profile); err != nil {
				t.Fatal(err)
			}
			cmd, err := s.CommandIn(context.Background(), root, []string{extra}, "echo hi")
			if err != nil {
				t.Fatal(err)
			}
			args := strings.Join(cmd.Args, " ") + " "
			for _, w := range c.want {
				if !strings.Contains(args, w) {
					t.Errorf("argv %q is missing %q", args, w)
				}
			}
			for _, w := range c.absent {
				if strings.Contains(args, w) {
					t.Errorf("argv %q should not contain %q", args, w)
				}
			}
			if !strings.HasSuffix(args, "sh -c echo hi ") {
				t.Errorf("argv %q does not end in the command", args)
			}
		})
	}
}

// TestSeatbeltPolicyPerProfile pins the sandbox-exec policy text each
// profile produces. It is pure text, so it is checked on every OS.
func TestSeatbeltPolicyPerProfile(t *testing.T) {
	root, extra := "/proj", "/extra"
	tmp := filepath.Clean(os.TempDir())
	cases := []struct {
		profile string
		want    []string
		absent  []string
	}{
		{"workspace", []string{
			"(deny default)",
			"(allow file-read*)",
			`(allow file-write* (subpath "/proj"))`,
			`(allow file-write* (subpath "/extra"))`,
			`(deny file-write* (subpath "/proj/.janetcode"))`,
			`(deny file-write* (regex #"/id_rsa$"))`,
			`(deny file-write* (regex #"/\.ssh/"))`,
			"(allow network*)",
		}, []string{"(deny network*)", `(deny file-write* (subpath "/proj/.git"))`}},
		{"read-only", []string{"(deny default)", "(allow file-read*)", "(allow network*)"},
			[]string{"(allow file-write* (subpath"}},
		{"strict", []string{
			`(allow file-write* (subpath "/proj"))`,
			`(deny file-write* (subpath "/proj/.git"))`,
			"(deny network*)",
		}, []string{"(allow network*)"}},
	}
	for _, c := range cases {
		t.Run(c.profile, func(t *testing.T) {
			p, ok := FindProfile(c.profile)
			if !ok {
				t.Fatalf("no profile %q", c.profile)
			}
			policy := SeatbeltPolicy(p, root, []string{extra})
			for _, w := range c.want {
				if !strings.Contains(policy, w) {
					t.Errorf("policy is missing %q:\n%s", w, policy)
				}
			}
			for _, w := range c.absent {
				if strings.Contains(policy, w) {
					t.Errorf("policy should not contain %q:\n%s", w, policy)
				}
			}
			if c.profile != "read-only" && !strings.Contains(policy, `(subpath "`+tmp+`")`) {
				t.Errorf("policy does not make the temp dir writable:\n%s", policy)
			}
		})
	}
}

// TestSetSandboxParsing covers the whole /sandbox argument surface.
func TestSetSandboxParsing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sandbox is not supported on windows")
	}
	s := stubSandbox(t, "", 0)
	if status, err := SetSandbox(s, ""); err != nil || !strings.Contains(status, "off") {
		t.Fatalf("report: status=%q err=%v", status, err)
	}
	if status, err := SetSandbox(s, "on"); err != nil || s.ProfileName() != DefaultProfile || !strings.Contains(status, "profile "+DefaultProfile) {
		t.Fatalf("on: status=%q profile=%q err=%v", status, s.ProfileName(), err)
	}
	for _, name := range ProfileNames() {
		status, err := SetSandbox(s, name)
		if err != nil {
			t.Fatalf("/sandbox %s: %v", name, err)
		}
		if s.ProfileName() != name || !s.Enabled() {
			t.Fatalf("/sandbox %s: profile=%q enabled=%v", name, s.ProfileName(), s.Enabled())
		}
		if !strings.Contains(status, "profile "+name) {
			t.Errorf("/sandbox %s reported %q", name, status)
		}
	}
	if _, err := SetSandbox(s, "read-onlyy"); err == nil {
		t.Error("a mistyped profile should not be taken for a docker image")
	}
	if status, err := SetSandbox(s, "off"); err != nil || !strings.Contains(status, "off") {
		t.Fatalf("off: status=%q err=%v", status, err)
	}
	if _, err := SetSandbox(s, "docker"); err != nil || s.Backend() != BackendDocker {
		t.Fatalf("backend: %v %q", err, s.Backend())
	}
	// The profile survives a backend switch and an image.
	if err := s.SetProfile("strict"); err != nil {
		t.Fatal(err)
	}
	if _, err := SetSandbox(s, "myimage:latest"); err != nil || s.Image() != "myimage:latest" || s.ProfileName() != "strict" {
		t.Fatalf("image: image=%q profile=%q err=%v", s.Image(), s.ProfileName(), err)
	}
	if err := s.SetProfile("nope"); err == nil {
		t.Error("SetProfile accepted an unknown profile")
	}
	if err := s.SetBackend("nope"); err == nil {
		t.Error("SetBackend accepted an unknown backend")
	}
}

// TestStrictRefusedOnOldLandlock: Landlock cannot take the network away
// before ABI 4, so strict refuses to run on such a kernel rather than
// pretending. The workspace profile is fine there.
func TestStrictRefusedOnOldLandlock(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Landlock is Linux only")
	}
	s := &Sandbox{
		look: func(string) (string, error) { return "", errors.New("not installed") },
		abi:  func() int { return 3 },
	}
	if err := s.SetProfile("strict"); err != nil {
		t.Fatal(err)
	}
	err := s.Enable()
	if err == nil || !strings.Contains(err.Error(), "ABI 4") {
		t.Fatalf("strict on ABI 3 gave %v, want a refusal naming ABI 4", err)
	}
	if err := s.SetProfile("workspace"); err != nil {
		t.Fatal(err)
	}
	if err := s.Enable(); err != nil {
		t.Fatalf("workspace on ABI 3: %v", err)
	}
	if s.Backend() != BackendLandlock {
		t.Fatalf("backend = %q, want landlock when docker is missing", s.Backend())
	}
	if notes := strings.Join(s.Notes(), " "); !strings.Contains(notes, "deny globs") {
		t.Errorf("notes %q do not admit that Landlock cannot enforce deny globs", notes)
	}
}

// TestDockerCommandEnvIsScrubbed: the container process used to inherit
// the parent's full environment, so a model command inside the sandbox
// could read $ANTHROPIC_API_KEY and write it into the writable workspace
// mount. The landlock and seatbelt backends scrub; docker now matches.
func TestDockerCommandEnvIsScrubbed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no sandbox on windows")
	}
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-docker-marker")
	root := t.TempDir()
	s := stubSandbox(t, BackendDocker, 0)
	if _, err := SetSandbox(s, "workspace"); err != nil {
		t.Fatal(err)
	}
	cmd, err := s.CommandIn(context.Background(), root, nil, "echo hi")
	if err != nil {
		t.Fatal(err)
	}
	if len(cmd.Env) == 0 {
		t.Fatal("container env is empty; the command needs a real environment")
	}
	for _, kv := range cmd.Env {
		if strings.Contains(kv, "sk-ant-docker-marker") {
			t.Fatalf("container env carries the agent's key: %q", kv)
		}
	}
}
