package main

import (
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
)

// binPrefix names the release binaries in a dist directory:
// janetcode-<version>-<os>-<arch>[.exe].
const binPrefix = "janetcode-"

// Artifact is one release binary, described from the file itself.
type Artifact struct {
	Name     string `json:"name"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size"`
	GOOS     string `json:"goos"`
	GOARCH   string `json:"goarch"`
	Go       string `json:"go_version"`
	Revision string `json:"vcs_revision"`
	Modified bool   `json:"vcs_modified"`
	Trimpath bool   `json:"trimpath"`
	CGO      string `json:"cgo_enabled"`
	// Version is what -ldflags -X main.version stamped, when it can be
	// read from the settings.
	LDFlags string `json:"ldflags,omitempty"`
	Deps    int    `json:"module_dependencies"`

	info *debug.BuildInfo
}

func setting(info *debug.BuildInfo, key string) string {
	for _, s := range info.Settings {
		if s.Key == key {
			return s.Value
		}
	}
	return ""
}

func fileSHA256(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// readArtifacts finds the binaries in dir and reads each one's build
// information, in name order.
func readArtifacts(dir string) ([]Artifact, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []Artifact
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), binPrefix) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		info, err := buildinfo.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("%s: not a Go binary with build information: %w", e.Name(), err)
		}
		sum, size, err := fileSHA256(path)
		if err != nil {
			return nil, err
		}
		out = append(out, Artifact{
			Name: e.Name(), SHA256: sum, Size: size,
			GOOS: setting(info, "GOOS"), GOARCH: setting(info, "GOARCH"), Go: info.GoVersion,
			Revision: setting(info, "vcs.revision"), Modified: setting(info, "vcs.modified") == "true",
			Trimpath: setting(info, "-trimpath") == "true", CGO: setting(info, "CGO_ENABLED"),
			LDFlags: setting(info, "-ldflags"), Deps: len(info.Deps), info: info,
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no %s* binaries in %s (run make dist first)", binPrefix, dir)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// git runs git in the current directory.
func git(args ...string) (string, error) {
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}

// writeJSON writes v indented, with a trailing newline, atomically enough
// for a build directory: a failed marshal leaves the old file alone.
func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}
