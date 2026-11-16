// Package opref resolves 1Password secret references (op://vault/item/field)
// with the `op` command line tool. The reference is what lives in the
// config; the value is read when something needs it and is never written
// back anywhere.
package opref

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/xmonader/janetcode/internal/secret"
)

// Prefix starts every reference.
const Prefix = "op://"

// timeout bounds one `op read`. A locked vault with a desktop sign-in
// waits for an approval nobody can give on a headless machine; this makes
// that a clear error instead of a hung start.
const timeout = 30 * time.Second

// ttl is how long a resolved value is reused. One run builds a provider
// per model and per subagent; asking 1Password each time would prompt for
// approval each time. A rotated credential is picked up after ttl.
const ttl = 5 * time.Minute

// IsRef reports whether s is a reference rather than a literal value.
func IsRef(s string) bool {
	s = strings.TrimSpace(s)
	return strings.HasPrefix(s, Prefix) && len(s) > len(Prefix)
}

type cached struct {
	val string
	at  time.Time
}

var (
	mu   sync.Mutex
	memo = map[string]cached{}
	now  = time.Now
	// read runs `op read ref`; tests replace it.
	read = runOp
)

// Resolve returns the value ref names. A value that is not a reference is
// returned unchanged. The error names the reference, never a value.
func Resolve(ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if !IsRef(ref) {
		return ref, nil
	}
	mu.Lock()
	if c, ok := memo[ref]; ok && now().Sub(c.at) < ttl {
		mu.Unlock()
		return c.val, nil
	}
	mu.Unlock()
	out, err := read(ref)
	if err != nil {
		return "", err
	}
	val := strings.TrimRight(out, "\r\n")
	if val == "" {
		return "", fmt.Errorf("1Password reference %s resolved to an empty value", ref)
	}
	// A resolved value has no recognisable shape; make sure nothing the
	// agent prints can carry it out.
	secret.Register(val)
	mu.Lock()
	memo[ref] = cached{val: val, at: now()}
	mu.Unlock()
	return val, nil
}

// ResolveAll resolves each entry of vals, leaving literals alone.
func ResolveAll(vals []string) ([]string, error) {
	out := make([]string, len(vals))
	for i, v := range vals {
		r, err := Resolve(v)
		if err != nil {
			return nil, err
		}
		out[i] = r
	}
	return out, nil
}

// Forget drops every resolved value, so the next use reads 1Password again.
func Forget() {
	mu.Lock()
	memo = map[string]cached{}
	mu.Unlock()
}

func runOp(ref string) (string, error) {
	bin, err := exec.LookPath("op")
	if err != nil {
		return "", fmt.Errorf("cannot read %s: the 1Password CLI (`op`) is not installed or not on PATH", ref)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "read", ref)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", fmt.Errorf("`op read %s` timed out after %s: is 1Password unlocked and signed in on this machine?", ref, timeout)
		}
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 300 {
			msg = msg[:300] + "..."
		}
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("`op read %s` failed: %s", ref, msg)
	}
	return stdout.String(), nil
}
