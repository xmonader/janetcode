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
