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
	"sync"
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

// maxStoreBytes bounds the file we are willing to parse: a trust store is
// a few hundred bytes per project, and a truncated or hostile file should
// cost a re-approval, not the heap.
const maxStoreBytes = 4 << 20

// writeMu serialises the read-modify-write in Trust. Store is a value
// copied by its users, so the lock cannot live on it; approvals are rare
// and the critical section is one small file, so one lock for the process
// is enough. Its only job is to stop two concurrent approvals from losing
// one another.
var writeMu sync.Mutex

// Store remembers which project files the user approved, keyed by
// directory and content hash, so an edited .mcp.json asks again.
//
// Reads are fail-closed: a missing, oversized, unreadable or corrupt
// store trusts nothing, which costs an approval prompt rather than
// silently approving something the user never saw.
type Store struct {
	Path string
}

func (t Store) load() (map[string]string, error) {
	m := map[string]string{}
	info, err := os.Stat(t.Path)
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err == nil && info.Size() > maxStoreBytes {
		return nil, fmt.Errorf("%s: trust store is %d bytes, over the %d limit", t.Path, info.Size(), maxStoreBytes)
	}
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
	if m == nil { // the file held a bare "null"
		m = map[string]string{}
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
	writeMu.Lock()
	defer writeMu.Unlock()
	m, err := t.load()
	if err != nil {
		return err
	}
	m[cwd] = hash
	return t.writeMap(m)
}

// writeMap writes the whole store; the caller holds writeMu.
func (t Store) writeMap(m map[string]string) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(t.Path), 0o755); err != nil {
		return err
	}
	return writeAtomic(t.Path, append(data, '\n'))
}

// writeAtomic replaces the store in one step, so an interrupted write
// cannot leave a half-file behind and lose every approval the user has
// ever given.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".trust-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() {
		f.Close()
		os.Remove(tmp) // no-op once the rename has succeeded
	}()
	// Best effort: CreateTemp already makes the file 0600 on unix, and on
	// Windows the mode is not meaningful.
	_ = f.Chmod(0o600)
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
