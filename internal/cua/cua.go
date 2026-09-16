// Package cua finds a Computer Use driver on this machine and describes
// how to run it as an MCP server. janetcode does not ship the driver:
// install Cua Driver (cua-driver) or @qwen-code/open-computer-use, then
// /cua on.
package cua

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/mcp"
)

// ServerName is the MCP server id used for whichever driver is found, so
// the skill and /cua can name mcp__cua-driver__* tools consistently.
const ServerName = "cua-driver"

// Driver is an installed Computer Use backend.
type Driver struct {
	// Kind is "cua-driver" or "open-computer-use".
	Kind string
	// Path is the executable.
	Path string
	Args []string
}

// LookPath is exec.LookPath; tests replace it.
var LookPath = exec.LookPath

// Detect reports the preferred installed driver, if any.
// Order: CUA_DRIVER, cua-driver, open-computer-use.
func Detect() (Driver, bool) {
	if p := strings.TrimSpace(os.Getenv("CUA_DRIVER")); p != "" {
		// Both drivers spell the subcommand "mcp".
		return Driver{Kind: kindOf(p), Path: p, Args: []string{"mcp"}}, true
	}
	if p, err := LookPath("cua-driver"); err == nil {
		return Driver{Kind: "cua-driver", Path: p, Args: []string{"mcp"}}, true
	}
	if p, err := LookPath("open-computer-use"); err == nil {
		return Driver{Kind: "open-computer-use", Path: p, Args: []string{"mcp"}}, true
	}
	return Driver{}, false
}

func kindOf(path string) string {
	base := path
	if i := strings.LastIndexAny(path, `/\`); i >= 0 {
		base = path[i+1:]
	}
	if strings.Contains(strings.ToLower(base), "open-computer-use") {
		return "open-computer-use"
	}
	return "cua-driver"
}

// Config is the MCP stdio server entry for this driver.
func (d Driver) Config() mcp.ServerConfig {
	return mcp.ServerConfig{Command: d.Path, Args: d.Args, Type: "stdio"}
}

// MergeInto adds the driver to servers when it is installed and not
// already listed under ServerName. The flag reports whether a driver was
// found — callers print the install hint when it is false — and not
// whether anything was added: a nil map has nothing added to it, but that
// says nothing about the machine.
func MergeInto(servers map[string]mcp.ServerConfig) (Driver, bool) {
	d, ok := Detect()
	if !ok {
		return Driver{}, false
	}
	if _, exists := servers[ServerName]; servers != nil && !exists {
		servers[ServerName] = d.Config()
	}
	return d, true
}

// InstallHint is what to print when no driver is on PATH.
const InstallHint = `Install Cua Driver and put cua-driver on PATH, then /cua on.
See https://cua.ai/docs/how-to-guides/driver/install
Alternatively: npm i -g @qwen-code/open-computer-use`

// Doctor runs the driver's own checks when it has any, otherwise just
// reports that the binary exists.
func (d Driver) Doctor(ctx context.Context) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s at %s\n", d.Kind, d.Path)
	if d.Kind == "cua-driver" {
		if out, err := runCmd(ctx, d.Path, "--version"); err == nil && out != "" {
			fmt.Fprintf(&sb, "version: %s\n", out)
		}
		if out, err := runCmd(ctx, d.Path, "permissions", "status"); err == nil && out != "" {
			sb.WriteString(out)
			if !strings.HasSuffix(out, "\n") {
				sb.WriteByte('\n')
			}
		}
	}
	if d.Kind == "open-computer-use" {
		if out, err := runCmd(ctx, d.Path, "doctor"); err == nil && out != "" {
			sb.WriteString(out)
			if !strings.HasSuffix(out, "\n") {
				sb.WriteByte('\n')
			}
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

func runCmd(ctx context.Context, name string, args ...string) (string, error) {
	c, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(c, name, args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return strings.TrimSpace(buf.String()), err
}
