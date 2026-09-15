// Package trust records which project-supplied files (.mcp.json, hooks,
// verification commands, permission rules) the user approved, tied to the
// exact content they approved, so an edited file has to be approved again.
package trust

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// The store also holds the approvals of other project files, keyed by the
// directory with a suffix so they stay apart from the MCP approval, which
// is keyed by the bare directory.

// HooksKey is the store key for a project's .janetcode/hooks.json.
func HooksKey(cwd string) string { return cwd + "#hooks" }

// VerifyKey is the store key for a project's verification command.
func VerifyKey(cwd string) string { return cwd + "#verify" }

// PermissionsKey is the store key for a project's permission rules.
func PermissionsKey(cwd string) string { return cwd + "#permissions" }

// Store remembers which project files the user approved, keyed by
// directory and content hash, so an edited .mcp.json asks again.
type Store struct {
	Path string
}

func (t Store) load() (map[string]string, error) {
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
func (t Store) Trusted(cwd, hash string) bool {
	if t.Path == "" || hash == "" {
		return false
	}
	m, err := t.load()
	return err == nil && m[cwd] == hash
}

// Trust records approval of the project file with this hash.
func (t Store) Trust(cwd, hash string) error {
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
