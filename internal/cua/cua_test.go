package cua

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/mcp"
)

func TestDetectPrefersCUADriverEnv(t *testing.T) {
	t.Setenv("CUA_DRIVER", "/opt/bin/cua-driver")
	d, ok := Detect()
	if !ok || d.Kind != "cua-driver" || d.Path != "/opt/bin/cua-driver" || strings.Join(d.Args, " ") != "mcp" {
		t.Fatalf("got %+v ok=%v", d, ok)
	}
}

func TestDetectFindsOpenComputerUse(t *testing.T) {
	t.Setenv("CUA_DRIVER", "")
	dir := t.TempDir()
	bin := filepath.Join(dir, "open-computer-use")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	orig := LookPath
	LookPath = func(name string) (string, error) {
		if name == "open-computer-use" {
			return bin, nil
		}
		return "", os.ErrNotExist
	}
	t.Cleanup(func() { LookPath = orig })
	d, ok := Detect()
	if !ok || d.Kind != "open-computer-use" || d.Path != bin {
		t.Fatalf("got %+v ok=%v", d, ok)
	}
}

func TestMergeIntoDoesNotOverwrite(t *testing.T) {
	t.Setenv("CUA_DRIVER", "/opt/bin/cua-driver")
	servers := map[string]mcp.ServerConfig{ServerName: {Command: "already"}}
	d, ok := MergeInto(servers)
	if !ok || d.Path != "/opt/bin/cua-driver" {
		t.Fatalf("detect failed: %+v %v", d, ok)
	}
	if servers[ServerName].Command != "already" {
		t.Fatalf("overwrote existing: %+v", servers[ServerName])
	}
	fresh := map[string]mcp.ServerConfig{}
	if _, ok := MergeInto(fresh); !ok || fresh[ServerName].Command != "/opt/bin/cua-driver" {
		t.Fatalf("did not add: %+v", fresh)
	}
}
