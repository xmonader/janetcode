package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/xmonader/janetcode/internal/shell"
)

// loadEnvrc replaces the project environment with dir's .envrc, for a
// project trusted at the full level only (an undecided project has no
// per-file approval for it: running someone's script is what the level
// is for). It returns a one-line note for /cd, empty when there is
// nothing to say. A failed or timed-out evaluation leaves no environment
// and says why on stderr; the filtered variables are named, never their
// values.
func (r *root) loadEnvrc(dir string) string {
	shell.SetProjectEnv(nil)
	if !r.cfg.EnvrcEnabled() || shell.EnvrcTimeout() <= 0 {
		return ""
	}
	if _, err := os.Stat(dir + "/.envrc"); err != nil {
		return ""
	}
	if r.gate == nil || !r.gate.Exec(dir, false) {
		return ".envrc here is not loaded: trust the project at the full level (/trust full)"
	}
	env, err := shell.LoadEnvrc(context.Background(), dir, shell.ScrubEnv(os.Environ()), shell.EnvrcTimeout())
	if err != nil {
		fmt.Fprintf(os.Stderr, "[envrc] not loaded: %v\n", err)
		return ".envrc not loaded: " + err.Error()
	}
	kept, dropped := shell.FilterEnvrc(env)
	shell.SetProjectEnv(kept)
	if len(dropped) > 0 {
		fmt.Fprintf(os.Stderr, "[envrc] refused to set %s\n", strings.Join(dropped, ", "))
	}
	if len(kept) == 0 {
		return ""
	}
	return fmt.Sprintf(".envrc: %d variable(s) set", len(kept))
}
