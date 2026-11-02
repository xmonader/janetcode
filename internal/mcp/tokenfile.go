package mcp

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// maxTokenFileBytes: a token is short, so a larger file is the wrong one.
// This, and the header-value check below, also keep a project-supplied
// config from sending a key file or a document to the server it names.
const maxTokenFileBytes = 16 * 1024

// tokenReadTimeout bounds a read from a stalled network mount.
const tokenReadTimeout = 5 * time.Second

// resolveTokenPath expands ${VAR} and a leading ~/ and demands the result
// be absolute, so a relative path cannot mean a different file in each
// directory the session moves through.
func resolveTokenPath(p string) (string, error) {
	p = os.ExpandEnv(strings.TrimSpace(p))
	if strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("token_file %s: no home directory", p)
		}
		p = filepath.Join(home, p[2:])
	}
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("token_file %q must be absolute or start with ~/", p)
	}
	return p, nil
}

// readTokenFile returns the file's contents, trimmed, as a bearer token.
// It is called for every request, so a token another process rotated
// takes effect on the next one without a reconnect. Errors name the path
// and never the contents.
func readTokenFile(path string) (string, error) {
	type result struct {
		tok string
		err error
	}
	ch := make(chan result, 1)
	go func() {
		tok, err := readTokenFileSync(path)
		ch <- result{tok, err}
	}()
	select {
	case r := <-ch:
		return r.tok, r.err
	case <-time.After(tokenReadTimeout):
		return "", fmt.Errorf("reading token_file %s timed out", path)
	}
}

func readTokenFileSync(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("token_file %s: %w", path, pathErr(err))
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxTokenFileBytes+1))
	if err != nil {
		return "", fmt.Errorf("token_file %s: %w", path, err)
	}
	if len(b) > maxTokenFileBytes {
		return "", fmt.Errorf("token_file %s is larger than %d bytes", path, maxTokenFileBytes)
	}
	tok := strings.TrimSpace(string(b))
	if tok == "" {
		return "", fmt.Errorf("token_file %s is empty", path)
	}
	for _, r := range tok {
		if r < 0x21 || r > 0x7e {
			return "", fmt.Errorf("token_file %s does not hold a bearer token (characters not allowed in an HTTP header)", path)
		}
	}
	return tok, nil
}

// pathErr drops the path an *os.PathError already carries.
func pathErr(err error) error {
	if pe, ok := err.(*os.PathError); ok {
		return pe.Err
	}
	return err
}
