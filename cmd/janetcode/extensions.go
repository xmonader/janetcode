package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/extension"
	"github.com/xmonader/janetcode/internal/hook"
	"github.com/xmonader/janetcode/internal/inspect"
	"github.com/xmonader/janetcode/internal/mcp"
	"github.com/xmonader/janetcode/internal/mcp/mcptool"
	"github.com/xmonader/janetcode/internal/skill"
	"github.com/xmonader/janetcode/internal/trust"
)

// The extension report (/doctor mcp, /inspect extensions, janetcode
// --doctor mcp) is built by internal/extension; this file only gathers
// what it reads from the machine and the session.

// extensionEnv is where the report's inputs come from. A running
// session sets mgr, and the report describes the servers it started; a
// bare command line leaves it nil and the report probes instead.
type extensionEnv struct {
	cfg     *config.Config
	cfgPath string
	cfgDir  string
	cwd     func() string
	gate    *trust.Gate
	ts      trust.Store
	safe    bool
	mgr     *mcptool.Manager
}

// probeEnabled reports whether a bare command line may start servers to
// describe them.
func probeEnabled() bool { return os.Getenv("JANETCODE_DOCTOR_NO_PROBE") == "" }

// verdict turns the project gate's answer for one executable file into
// the report's Verdict, with the sentence that says why.
func (e extensionEnv) verdict(cwd, key, hash, what string) extension.Verdict {
	if e.safe {
		return extension.Verdict{Why: "--safe-mode ignores everything the project supplies"}
	}
	perFile := e.ts.Trusted(key, hash)
	st := e.gate.State(cwd)
	ok := e.gate.Exec(cwd, perFile)
	switch {
	case st.Decided:
		why := fmt.Sprintf("project trust level %s (%s)", st.Level, st.Source)
		if !ok {
			why += "; /trust full lets " + what + " run"
		}
		return extension.Verdict{Trusted: ok, Why: why}
	case perFile:
		return extension.Verdict{Trusted: true, Why: what + " approved by hash (per-file approval); an edit asks again"}
	}
	return extension.Verdict{Why: what + " has no approval yet (/trust full, or /mcp trust for the servers)"}
}

func (e extensionEnv) inputs() (extension.Inputs, error) {
	cwd := e.cwd()
	in := extension.Inputs{
		Version: version, Cwd: cwd, ConfigPath: e.cfgPath,
		Store:      extension.Store{Path: extension.StorePath(e.cfgDir)},
		Tokens:     &mcp.TokenStore{Path: filepath.Join(e.cfgDir, "mcp-auth.json")},
		DeferAbove: mcptool.DefaultDeferAbove,
	}
	if e.cfg != nil {
		in.Global = e.cfg.MCPServers
		in.UserHooks = e.cfg.Hooks
	}
	in.HooksSource = e.cfgPath
	if e.safe {
		// Nothing of the project is read in safe mode, so nothing of
		// it is reported as if it could run.
		in.ProjectMCP = e.verdict(cwd, cwd, "", ".mcp.json")
		in.ProjectHooksOK = e.verdict(cwd, trust.HooksKey(cwd), "", "the hooks file")
	} else {
		servers, hash, err := mcp.LoadProject(cwd)
		if err != nil {
			return in, err
		}
		in.Project = servers
		in.ProjectMCP = e.verdict(cwd, cwd, hash, ".mcp.json")
		hooks, hhash, herr := hook.LoadProject(cwd)
		if herr != nil {
			return in, herr
		}
		in.ProjectHooks = hooks
		in.ProjectHooksOK = e.verdict(cwd, trust.HooksKey(cwd), hhash, "the hooks file")
	}
	instr := e.gate.Instructions(cwd) && !e.safe
	why := fmt.Sprintf("project trust level %s", e.gate.State(cwd).Level)
	if e.safe {
		why = "--safe-mode ignores the project's skills"
	}
	in.SkillTrust = extension.Verdict{Trusted: instr, Why: why}
	in.Skills = skill.WithBuiltins(skill.Load(cwd, e.cfgDir))

	if e.mgr != nil {
		in.Live = e.live
		return in, nil
	}
	if probeEnabled() {
		in.Probe = func(ctx context.Context, name string, cfg mcp.ServerConfig) (*mcp.Client, error) {
			return mcp.Start(ctx, name, cfg, mcp.Options{Root: cwd, Version: version, Tokens: in.Tokens, HandshakeTimeout: probeTimeout})
		}
	}
	return in, nil
}

// live reads one server's state from the session's manager.
func (e extensionEnv) live(name string) (extension.LiveServer, bool) {
	for _, st := range e.mgr.Status() {
		if st.Name != name {
			continue
		}
		lv := extension.LiveServer{State: "running", Err: st.Err}
		switch {
		case st.Down:
			lv.State = "down"
		case st.Starting:
			lv.State = "starting"
		case st.Err != nil:
			lv.State = "failed"
		}
		if c, ok := e.mgr.Client(name); ok {
			lv.Client = c
		}
		return lv, true
	}
	return extension.LiveServer{}, false
}

// report renders the report as text, or as JSON when the argument says
// json. It is the one function behind every front end.
func (e extensionEnv) report(ctx context.Context, arg string) (string, error) {
	asJSON := false
	switch strings.ToLower(strings.TrimSpace(arg)) {
	case "":
	case "json":
		asJSON = true
	default:
		return "", fmt.Errorf("unknown argument %q: want nothing, or json", arg)
	}
	in, err := e.inputs()
	if err != nil {
		return "", err
	}
	rep := extension.Build(ctx, in)
	if asJSON {
		return rep.JSON(), nil
	}
	return strings.TrimRight(rep.Text(), "\n"), nil
}

// registerExtensionsSubject adds /inspect extensions. /doctor mcp in the
// terminal and the browser is the same subject (inspect.DoctorSubject).
func registerExtensionsSubject(reg *inspect.Registry, e extensionEnv) {
	reg.Add(inspect.Subject{
		Name:    "extensions",
		Summary: "every MCP server, hook and skill: where it is declared, whether it is trusted and why, what it may do, and what it offers",
		Usage:   "[json]",
		Report:  e.report,
	})
}

// extensionsDoctorMode is janetcode --doctor mcp: the same report with
// no session behind it, so servers that are not running are probed with
// the handshake and the list requests only.
func extensionsDoctorMode(ctx context.Context, w io.Writer, ov config.Overrides, trustLevel string, asJSON bool) error {
	cfgPath, err := config.Path()
	if err != nil {
		return err
	}
	cfg, err := config.Load(ov)
	if err != nil && !errors.Is(err, config.ErrNoCredentials) {
		return err
	}
	cfgDir := filepath.Dir(cfgPath)
	gate, err := newTrustGate(cfgDir, trustLevel, os.Getenv)
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	env := extensionEnv{cfg: cfg, cfgPath: cfgPath, cfgDir: cfgDir, cwd: func() string { return cwd },
		gate: gate, ts: trust.Store{Path: filepath.Join(cfgDir, "mcp-trust.json")}}
	arg := ""
	if asJSON {
		arg = "json"
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	out, err := env.report(ctx, arg)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, out)
	return err
}
