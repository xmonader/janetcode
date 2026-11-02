package shell

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xmonader/janetcode/internal/procgroup"
)

// A project's .envrc is code: loading it is only done for a project the
// user trusted at the Full level (internal/trust). The loader mirrors
// direnv's own contract: `direnv export json` when direnv is installed,
// else bash with direnv's helper functions stubbed. Every wait is
// bounded -- the whole evaluation by a deadline, the output by a size
// cap and a pipe-drain grace -- so a .envrc that hangs, or leaves a
// background child holding the pipe, costs the environment and never
// freezes the session. Nothing partial is ever installed.

const (
	// EnvrcTimeoutEnv overrides the evaluation deadline, in seconds; 0
	// disables .envrc loading.
	EnvrcTimeoutEnv     = "JANETCODE_ENVRC_TIMEOUT_SECS"
	defaultEnvrcTimeout = 10 * time.Second
	maxEnvrcTimeout     = time.Hour
	envrcPipeGrace      = 250 * time.Millisecond
	maxEnvrcOutput      = 4 << 20
)

// EnvrcTimeout is the evaluation deadline: 10s, or the env override
// (clamped to an hour; unparseable values are ignored; 0 disables).
func EnvrcTimeout() time.Duration {
	v := strings.TrimSpace(os.Getenv(EnvrcTimeoutEnv))
	if v == "" {
		return defaultEnvrcTimeout
	}
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		return defaultEnvrcTimeout
	}
	if d := time.Duration(n) * time.Second; n <= uint64(maxEnvrcTimeout/time.Second) {
		return d
	}
	return maxEnvrcTimeout
}

// direnvStubs stand in for direnv's stdlib when direnv is not installed:
// enough for the .envrc files that only export variables and extend PATH.
const direnvStubs = `
source_up_if_exists() { :; }
source_up() { :; }
source_env_if_exists() { if [ -f "$1" ]; then . "$1"; fi; }
source_env() { if [ -f "$1" ]; then . "$1"; fi; }
PATH_add() { export PATH="$PWD/$1:$PATH"; }
path_add() { PATH_add "$@"; }
layout() { :; }
use() { :; }
watch_file() { :; }
dotenv() { :; }
`

// LoadEnvrc evaluates dir/.envrc and returns the variables it sets or
// changes against base (os.Environ form). A directory with no .envrc,
// an evaluation that fails, times out or is cut short all return an
// empty map and, for the latter three, an error to report.
func LoadEnvrc(ctx context.Context, dir string, base []string, timeout time.Duration) (map[string]string, error) {
	if timeout <= 0 {
		return nil, nil
	}
	path := filepath.Join(dir, ".envrc")
	fi, err := os.Stat(path)
	if err != nil {
		return nil, nil
	}
	if !fi.Mode().IsRegular() { // a FIFO would block the evaluator forever
		return nil, errors.New(".envrc is not a regular file")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if _, err := exec.LookPath("direnv"); err == nil {
		// direnv answers for itself: a refusal ("not allowed") or any
		// failure means no environment, never a bash re-run of a file
		// direnv declined to evaluate.
		out, err := runBounded(ctx, dir, base, "direnv", "export", "json")
		if err != nil {
			return nil, fmt.Errorf("direnv: %w", err)
		}
		return parseDirenvJSON(out)
	}
	sentinel := make([]byte, 8)
	_, _ = rand.Read(sentinel)
	mark := "__JANETCODE_ENVRC_" + hex.EncodeToString(sentinel)
	script := "set -e\n" + direnvStubs + `. "$1"` + "\nenv -0\nprintf '%s' '" + mark + "'\n"
	out, err := runBounded(ctx, dir, base, "/bin/bash", "-c", script, "bash", path)
	if err != nil {
		return nil, fmt.Errorf(".envrc: %w", err)
	}
	return parseBashEnv(out, mark, base)
}

// runBounded runs a command in its own process group under ctx, capping
// what it may print and how long a descendant may hold the pipes open.
func runBounded(ctx context.Context, dir string, env []string, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdin = nil
	procgroup.KillOnCancel(cmd)
	cmd.WaitDelay = envrcPipeGrace
	out := &capBuffer{max: maxEnvrcOutput}
	cmd.Stdout = out
	cmd.Stderr = nil
	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, errors.New("timed out")
	}
	if out.over {
		return nil, errors.New("output over the size cap")
	}
	if err != nil && !errors.Is(err, exec.ErrWaitDelay) {
		return nil, err
	}
	return out.buf.Bytes(), nil
}

type capBuffer struct {
	buf  bytes.Buffer // not embedded: its ReadFrom would let io.Copy skip the cap
	max  int
	over bool
}

func (b *capBuffer) Write(p []byte) (int, error) {
	if b.buf.Len()+len(p) > b.max {
		b.over = true
		return len(p), nil
	}
	return b.buf.Write(p)
}

func parseDirenvJSON(out []byte) (map[string]string, error) {
	if len(bytes.TrimSpace(out)) == 0 {
		return nil, nil
	}
	var raw map[string]any
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, errors.New("direnv printed something that is not JSON")
	}
	env := map[string]string{}
	for k, v := range raw {
		if s, ok := v.(string); ok { // null means unset; ignored
			env[k] = s
		}
	}
	return env, nil
}

// ignoredEnvrcVars are the shell's own bookkeeping, not the .envrc's.
var ignoredEnvrcVars = map[string]bool{"_": true, "SHLVL": true, "PWD": true, "OLDPWD": true}

func parseBashEnv(out []byte, mark string, base []string) (map[string]string, error) {
	i := bytes.Index(out, append([]byte{0}, mark...))
	if i < 0 {
		if bytes.HasPrefix(out, []byte(mark)) {
			return nil, nil
		}
		return nil, errors.New("output was cut short; discarded")
	}
	have := map[string]string{}
	for _, kv := range base {
		k, v, _ := strings.Cut(kv, "=")
		have[k] = v
	}
	env := map[string]string{}
	for _, e := range strings.Split(string(out[:i]), "\x00") {
		k, v, ok := strings.Cut(e, "=")
		if !ok || k == "" || ignoredEnvrcVars[k] {
			continue
		}
		if old, same := have[k]; same && old == v {
			continue
		}
		env[k] = v
	}
	return env, nil
}

// injectionVars are loader and interpreter hooks that run code inside
// every process or command that follows. A .envrc may set paths and
// tokens; it may not plant these. The same family safety.go refuses
// when a command line tries `env BASH_ENV=...`.
var injectionVars = map[string]bool{
	"BASH_ENV": true, "ENV": true, "SHELLOPTS": true, "BASHOPTS": true, "PS4": true,
	"PROMPT_COMMAND": true, "IFS": true, "CDPATH": true, "GLOBIGNORE": true, "BASH_XTRACEFD": true,
	"GIT_EXTERNAL_DIFF": true, "GIT_SSH_COMMAND": true, "GIT_SSH": true, "GIT_ASKPASS": true,
	"GIT_PROXY_COMMAND": true, "GIT_CONFIG_GLOBAL": true, "GIT_CONFIG_SYSTEM": true,
	"GIT_CONFIG_COUNT": true, "GIT_CONFIG_PARAMETERS": true, "GIT_EDITOR": true, "GIT_PAGER": true,
	"PYTHONSTARTUP": true, "PYTHONPATH": true, "NODE_OPTIONS": true, "PERL5OPT": true, "PERL5LIB": true,
	"RUBYOPT": true, "RUBYLIB": true, "JAVA_TOOL_OPTIONS": true, "_JAVA_OPTIONS": true,
	"JDK_JAVA_OPTIONS": true, "HOME": true, "SHELL": true,
}

// FilterEnvrc keeps what a .envrc may set, as sorted NAME=value pairs,
// and returns the names it refused. Refused: loader hooks (LD_*, DYLD_*,
// BASH_FUNC_*), the injection family above, the agent's own credentials
// (the names ScrubEnv strips), anything holding a NUL, and names that
// are not shell identifiers.
func FilterEnvrc(env map[string]string) (kept []string, dropped []string) {
	for k, v := range env {
		if envrcRefused(k, v) {
			dropped = append(dropped, k)
			continue
		}
		kept = append(kept, k+"="+v)
	}
	sort.Strings(kept)
	sort.Strings(dropped)
	return kept, dropped
}

func envrcRefused(k, v string) bool {
	if k == "" || strings.ContainsAny(v, "\x00") || strings.HasPrefix(k, "JANETCODE_") {
		return true
	}
	for i, r := range k {
		ok := r == '_' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (i > 0 && r >= '0' && r <= '9')
		if !ok {
			return true
		}
	}
	up := strings.ToUpper(k)
	return injectionVars[up] || strings.HasPrefix(up, "LD_") || strings.HasPrefix(up, "DYLD_") ||
		strings.HasPrefix(up, "BASH_FUNC_") || isSecretName(k)
}

var (
	projectEnvMu sync.Mutex
	projectEnv   []string
)

// SetProjectEnv installs the filtered .envrc environment: every command
// built through ScrubEnv (the bash tool, hooks, verification) sees it,
// under the scrub's own rules. nil removes it.
func SetProjectEnv(kv []string) {
	projectEnvMu.Lock()
	defer projectEnvMu.Unlock()
	projectEnv = append([]string(nil), kv...)
}

// ProjectEnv is what SetProjectEnv installed.
func ProjectEnv() []string {
	projectEnvMu.Lock()
	defer projectEnvMu.Unlock()
	return append([]string(nil), projectEnv...)
}

// overlayEnv lays the project environment over env, an entry replacing
// the one of the same name.
func overlayEnv(env []string) []string {
	over := ProjectEnv()
	if len(over) == 0 {
		return env
	}
	names := map[string]bool{}
	for _, kv := range over {
		n, _, _ := strings.Cut(kv, "=")
		names[n] = true
	}
	out := make([]string, 0, len(env)+len(over))
	for _, kv := range env {
		if n, _, _ := strings.Cut(kv, "="); !names[n] {
			out = append(out, kv)
		}
	}
	return append(out, over...)
}
