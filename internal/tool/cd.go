package tool

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ResolveDir turns a /cd argument into an absolute, existing directory.
//
// It resolves against cur — where the session is rooted now — rather than
// the process working directory, because a session that has already moved
// must read "../sibling" from where it is, not from where janetcode was
// started. "~" and "~/x" expand to the home directory. The result is
// refused unless it is a directory the process can actually open, so a
// typo, a file, or a directory with no search permission is reported here
// instead of surfacing later as a tool call that cannot read anything.
func ResolveDir(cur, arg string) (string, error) {
	p := strings.TrimSpace(arg)
	// A path pasted from a shell often arrives quoted.
	if len(p) >= 2 && (p[0] == '"' || p[0] == '\'') && p[len(p)-1] == p[0] {
		p = p[1 : len(p)-1]
	}
	if p == "" {
		return "", errors.New("no directory given")
	}
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("~: %w", err)
		}
		p = filepath.Join(home, strings.TrimLeft(p[1:], `/\`))
	}
	if !filepath.IsAbs(p) {
		base := cur
		if base == "" {
			base, _ = os.Getwd()
		}
		p = filepath.Join(base, p)
	}
	p = filepath.Clean(p)
	st, err := os.Stat(p)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "", fmt.Errorf("%s: no such directory", p)
	case err != nil:
		return "", err
	case !st.IsDir():
		return "", fmt.Errorf("%s is not a directory", p)
	}
	// Stat succeeds on a directory the process may not list or enter;
	// opening it is what tells the two apart.
	f, err := os.Open(p)
	if err != nil {
		return "", fmt.Errorf("%s: %w", p, err)
	}
	_ = f.Close()
	return p, nil
}
