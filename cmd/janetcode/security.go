package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/extension"
	"github.com/xmonader/janetcode/internal/inspect"
	"github.com/xmonader/janetcode/internal/secreport"
	"github.com/xmonader/janetcode/internal/shell"
	"github.com/xmonader/janetcode/internal/trust"
	"github.com/xmonader/janetcode/internal/xdg"
)

// securityEnv is where `--doctor security` reads from. A running session
// sets sandbox and ext so the report reflects what /sandbox and /mcp did
// since startup; a bare command line leaves them nil.
type securityEnv struct {
	cfg         *config.Config
	cfgErr      error
	cfgPath     string
	cfgDir      string
	sandboxFlag bool
	sandbox     *shell.Sandbox
	ext         extensionEnv
}

func (e securityEnv) report(ctx context.Context, arg string) (string, error) {
	asJSON := false
	switch strings.ToLower(strings.TrimSpace(arg)) {
	case "":
	case "json":
		asJSON = true
	default:
		return "", fmt.Errorf("unknown argument %q: want nothing, or json", arg)
	}
	rep, err := e.build(ctx)
	if err != nil {
		return "", err
	}
	if asJSON {
		return rep.JSON(), nil
	}
	return strings.TrimRight(rep.Text(), "\n"), nil
}

func (e securityEnv) build(ctx context.Context) (secreport.Report, error) {
	// The extension report is read with probing off: this report
	// summarises trust and configuration, it does not start servers.
	in, err := e.ext.inputs()
	if err != nil {
		return secreport.Report{}, err
	}
	in.Probe = nil
	extRep := extension.Build(ctx, in)

	exe, _ := os.Executable()
	data, _ := xdg.DataDir(os.Getenv)
	si := secreport.Inputs{
		Version: version, Cfg: e.cfg, CfgErr: e.cfgErr, CfgPath: e.cfgPath, DataDir: data,
		Exe: exe, Sandbox: e.sandbox, SandboxFlag: e.sandboxFlag, Extensions: extRep,
		TrustStore: filepath.Join(e.cfgDir, "mcp-trust.json"),
	}
	if e.sandbox != nil {
		si.SandboxOn = e.sandbox.Enabled()
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		si.BuildInfo = bi
	}
	return secreport.Build(si), nil
}

// registerSecuritySubject adds /inspect security, which /doctor security
// shows too.
func registerSecuritySubject(reg *inspect.Registry, e securityEnv) {
	reg.Add(inspect.Subject{
		Name:    "security",
		Summary: "the local binary, sandbox capability and choice, trust stores, provider endpoints, MCP configuration and privacy settings; no credential is printed",
		Usage:   "[json]",
		Report:  e.report,
	})
}

// securityDoctorMode is janetcode --doctor security. It exits non-zero
// when a check fails (a requested sandbox the machine cannot provide, a
// world-writable approval store, a key sent over plain http), so a
// script or CI can gate on it.
func securityDoctorMode(ctx context.Context, w io.Writer, ov config.Overrides, trustLevel string, sandboxFlag, asJSON bool) error {
	cfgPath, err := config.Path()
	if err != nil {
		return err
	}
	// A configuration that does not load is itself a finding the report
	// states; the rest of it still stands, so the error is passed on.
	cfg, cfgErr := config.Load(ov)
	cfgDir := filepath.Dir(cfgPath)
	gate, err := newTrustGate(cfgDir, trustLevel, os.Getenv)
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	e := securityEnv{cfg: cfg, cfgErr: cfgErr, cfgPath: cfgPath, cfgDir: cfgDir, sandboxFlag: sandboxFlag, sandbox: &shell.Sandbox{},
		ext: extensionEnv{cfg: cfg, cfgPath: cfgPath, cfgDir: cfgDir, cwd: func() string { return cwd }, gate: gate,
			ts: trust.Store{Path: filepath.Join(cfgDir, "mcp-trust.json")}}}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	rep, err := e.build(ctx)
	if err != nil {
		return err
	}
	out := rep.Text()
	if asJSON {
		out = rep.JSON() + "\n"
	}
	if _, err := io.WriteString(w, out); err != nil {
		return err
	}
	if rep.Failed > 0 {
		return fmt.Errorf("%s failed", pluralChecks(rep.Failed))
	}
	return nil
}

func pluralChecks(n int) string {
	if n == 1 {
		return "1 security check"
	}
	return fmt.Sprintf("%d security checks", n)
}
