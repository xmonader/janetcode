package shell

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func envrcDir(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".envrc"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func skipIfDirenv(t *testing.T) {
	if _, err := exec.LookPath("direnv"); err == nil {
		t.Skip("direnv is installed: the bash fallback is not taken")
	}
}

func TestEnvrcTimeout(t *testing.T) {
	for _, c := range []struct {
		in   string
		want time.Duration
	}{
		{"", 10 * time.Second}, {"3", 3 * time.Second}, {"0", 0},
		{"abc", 10 * time.Second}, {"99999999", time.Hour}, {" 5 ", 5 * time.Second},
	} {
		t.Setenv(EnvrcTimeoutEnv, c.in)
		if got := EnvrcTimeout(); got != c.want {
			t.Errorf("%q: %v want %v", c.in, got, c.want)
		}
	}
}

func TestLoadEnvrcBashFallback(t *testing.T) {
	skipIfDirenv(t)
	base := os.Environ()
	dir := envrcDir(t, "export FOO=bar\nexport EMPTY=\nPATH_add bin\nsource_env_if_exists nothere\nlayout python\n")
	env, err := LoadEnvrc(context.Background(), dir, base, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if env["FOO"] != "bar" {
		t.Errorf("FOO: %v", env)
	}
	if _, ok := env["EMPTY"]; !ok {
		t.Errorf("an empty value is still a value: %v", env)
	}
	if !strings.HasPrefix(env["PATH"], filepath.Join(dir, "bin")+":") {
		t.Errorf("PATH_add stub: %q", env["PATH"])
	}
	for _, noise := range []string{"_", "SHLVL", "PWD", "OLDPWD", "HOME"} {
		if _, ok := env[noise]; ok {
			t.Errorf("unchanged or noise var %s reported", noise)
		}
	}
}

func TestLoadEnvrcFailuresInstallNothing(t *testing.T) {
	skipIfDirenv(t)
	base := os.Environ()
	for name, c := range map[string]struct {
		body string
		want string
	}{
		"errors": {"export A=1\nfalse\n", ".envrc"},
		"hangs":  {"export A=1\nwhile :; do :; done\n", "timed out"},
		"output": {"export A=1\nhead -c 9000000 /dev/zero | tr '\\0' x\n", "size cap"},
	} {
		dir := envrcDir(t, c.body)
		start := time.Now()
		env, err := LoadEnvrc(context.Background(), dir, base, time.Second)
		if err == nil || !strings.Contains(err.Error(), c.want) || len(env) != 0 {
			t.Errorf("%s: env %v err %v", name, env, err)
		}
		if time.Since(start) > 5*time.Second {
			t.Errorf("%s: not bounded: %v", name, time.Since(start))
		}
	}
}

func TestLoadEnvrcAbsentOrDisabled(t *testing.T) {
	if env, err := LoadEnvrc(context.Background(), t.TempDir(), nil, time.Second); env != nil || err != nil {
		t.Errorf("no .envrc: %v %v", env, err)
	}
	dir := envrcDir(t, "export A=1\n")
	if env, err := LoadEnvrc(context.Background(), dir, nil, 0); env != nil || err != nil {
		t.Errorf("zero timeout disables: %v %v", env, err)
	}
}

func TestParseDirenvJSON(t *testing.T) {
	got, err := parseDirenvJSON([]byte(`{"A":"1","GONE":null,"N":3}`))
	if err != nil || !reflect.DeepEqual(got, map[string]string{"A": "1"}) {
		t.Errorf("%v %v", got, err)
	}
	if got, err := parseDirenvJSON([]byte("  \n")); got != nil || err != nil {
		t.Errorf("empty: %v %v", got, err)
	}
	if _, err := parseDirenvJSON([]byte("{trunc")); err == nil {
		t.Error("bad JSON accepted")
	}
}

func TestParseBashEnvRejectsTruncation(t *testing.T) {
	good := []byte("A=1\x00B=2\x00MARK")
	if env, err := parseBashEnv(good, "MARK", []string{"B=2"}); err != nil || !reflect.DeepEqual(env, map[string]string{"A": "1"}) {
		t.Errorf("%v %v", env, err)
	}
	if _, err := parseBashEnv([]byte("A=1\x00B="), "MARK", nil); err == nil {
		t.Error("a capture with no sentinel must be discarded")
	}
}

func TestFilterEnvrc(t *testing.T) {
	kept, dropped := FilterEnvrc(map[string]string{
		"FOO": "bar", "PATH": "/x:/usr/bin", "AWS_PROFILE": "dev",
		"LD_PRELOAD": "/evil.so", "LD_LIBRARY_PATH": "/evil", "DYLD_INSERT_LIBRARIES": "x",
		"BASH_ENV": "/evil.sh", "BASH_FUNC_ls%%": "() { :; }", "PROMPT_COMMAND": "x",
		"GIT_EXTERNAL_DIFF": "x", "NODE_OPTIONS": "--require x", "HOME": "/tmp/h",
		"OPENAI_API_KEY": "sk-x", "DEEPSEEK_API_KEY_PERSONAL": "k", "JANETCODE_WEB_TOKEN": "t",
		"BAD NAME": "x", "NUL": "a\x00b", "1ST": "x",
	})
	wantKept := []string{"AWS_PROFILE=dev", "FOO=bar", "PATH=/x:/usr/bin"}
	if !reflect.DeepEqual(kept, wantKept) {
		t.Errorf("kept %v want %v", kept, wantKept)
	}
	if len(dropped) != 15 {
		t.Errorf("dropped %v", dropped)
	}
}

func TestScrubEnvAppliesProjectEnv(t *testing.T) {
	SetProjectEnv([]string{"FOO=envrc", "PAGER=evil"})
	defer SetProjectEnv(nil)
	out := ScrubEnv([]string{"FOO=old", "KEEP=1"})
	has := func(kv string) bool {
		for _, e := range out {
			if e == kv {
				return true
			}
		}
		return false
	}
	if !has("FOO=envrc") || has("FOO=old") || !has("KEEP=1") {
		t.Errorf("overlay not applied: %v", out)
	}
	if has("PAGER=evil") || !has("PAGER=cat") {
		t.Errorf("the non-interactive settings must win over the overlay: %v", out)
	}
}
