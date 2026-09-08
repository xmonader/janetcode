package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ProjectFile is the per-project server list, in the format Claude Code and
// other agents share: {"mcpServers": {"name": {"command": ..., "args": [...]}}}.
const ProjectFile = ".mcp.json"

// LoadProject reads <cwd>/.mcp.json. The hash identifies the exact contents
// so trust can be tied to them. A missing file yields no servers and no
// error.
func LoadProject(cwd string) (servers map[string]ServerConfig, hash string, err error) {
	data, err := os.ReadFile(filepath.Join(cwd, ProjectFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	var f struct {
		Servers map[string]ServerConfig `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, "", fmt.Errorf("%s: %w", ProjectFile, err)
	}
	sum := sha256.Sum256(data)
	return f.Servers, hex.EncodeToString(sum[:]), nil
}

// TrustStore remembers which project files the user approved, keyed by
// directory and content hash, so an edited .mcp.json asks again.
type TrustStore struct {
	Path string
}

func (t TrustStore) load() (map[string]string, error) {
	m := map[string]string{}
	data, err := os.ReadFile(t.Path)
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", t.Path, err)
	}
	return m, nil
}

// Trusted reports whether this exact project file was approved before.
func (t TrustStore) Trusted(cwd, hash string) bool {
	if t.Path == "" || hash == "" {
		return false
	}
	m, err := t.load()
	return err == nil && m[cwd] == hash
}

// Trust records approval of the project file with this hash.
func (t TrustStore) Trust(cwd, hash string) error {
	if t.Path == "" {
		return errors.New("no trust store path")
	}
	m, err := t.load()
	if err != nil {
		return err
	}
	m[cwd] = hash
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(t.Path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(t.Path, append(data, '\n'), 0o600)
}
