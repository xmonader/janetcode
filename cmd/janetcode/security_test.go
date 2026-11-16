package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/headless"
	"github.com/xmonader/janetcode/internal/inspect"
	"github.com/xmonader/janetcode/internal/secreport"
	"github.com/xmonader/janetcode/internal/shell"
	"github.com/xmonader/janetcode/internal/trust"
)

// securityProject is extProject with secrets planted everywhere a
// credential can sit: the config file, the environment, a URL, an MCP
// server's environment and arguments.
func securityProject(t *testing.T) (cwd, cfgDir string, secrets []string) {
	t.Helper()
	cwd, cfgDir = extProject(t)
	secrets = []string{"CFGKEYSECRET1234567890abc", "ENVKEYSECRET1234567890abc", "URLUSERPASS987654", "QUERYTOKEN987654321", "sekret-value-123456789", "ARGSECRET55555555"}
	t.Setenv("ANTHROPIC_API_KEY", secrets[1])
	cfg := map[string]any{
		"provider": "gw", "model": "m",
		"providers": map[string]any{"gw": map[string]any{"type": "openai", "base_url": "https://u:" + secrets[2] + "@gw.example.test/v1?api_key=" + secrets[3], "api_key": secrets[0]}},
		"mcp_servers": map[string]any{
			"userfake": map[string]any{"command": os.Args[0], "args": []string{"--token=" + secrets[5]}, "env": map[string]string{"MCP_FAKE_SERVER": "1", "API_KEY": secrets[4]}},
		},
	}
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return cwd, cfgDir, secrets
}

// --doctor security reports where credentials come from and prints none
// of them, in text or JSON, and carries the sandbox's state.
func TestDoctorSecurityPrintsNoCredential(t *testing.T) {
	_, _, secrets := securityProject(t)
	for _, asJSON := range []bool{false, true} {
		var out bytes.Buffer
		err := securityDoctorMode(context.Background(), &out, config.Overrides{}, "", false, asJSON)
		if err != nil {
			t.Fatalf("json=%v: %v\n%s", asJSON, err, out.String())
		}
		for _, s := range secrets {
			if strings.Contains(out.String(), s) {
				t.Fatalf("json=%v: the report printed %q:\n%s", asJSON, s, out.String())
			}
		}
		if asJSON {
			var rep secreport.Report
			if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
				t.Fatalf("not a report: %v\n%s", err, out.String())
			}
			if rep.Sandbox.State == "" || len(rep.Sandbox.Backends) == 0 || rep.Binary.Go == "" {
				t.Fatalf("report = %+v", rep)
			}
			var gw secreport.Check
			for _, c := range rep.Checks {
				if c.Name == "provider gw" {
					gw = c
				}
			}
			if !strings.Contains(gw.Detail, "the config file") || !strings.Contains(gw.Detail, "gw.example.test") {
				t.Fatalf("provider row = %+v", gw)
			}
		} else {
			for _, want := range []string{"Binary", "Sandbox", "Trust", "Providers", "MCP and hooks", "Privacy", "ANTHROPIC_API_KEY"} {
				if !strings.Contains(out.String(), want) {
					t.Fatalf("text lacks %q:\n%s", want, out.String())
				}
			}
		}
	}
}

// A failing check is the exit status: a world-writable approval store
// lets anyone approve their own project files.
func TestDoctorSecurityFailsOnAWritableApprovalStore(t *testing.T) {
	_, cfgDir, _ := securityProject(t)
	store := filepath.Join(cfgDir, "mcp-trust.json")
	if err := os.WriteFile(store, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(store, 0o666); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := securityDoctorMode(context.Background(), &out, config.Overrides{}, "", false, false)
	if err == nil || !strings.Contains(err.Error(), "security check") || !strings.Contains(out.String(), "writable by others") {
		t.Fatalf("err = %v\n%s", err, out.String())
	}
}

// --sandbox asks for a sandbox; whether this machine can give one is the
// report's business, and it keeps the two apart.
func TestDoctorSecuritySandboxChoiceVersusCapability(t *testing.T) {
	securityProject(t)
	var out bytes.Buffer
	_ = securityDoctorMode(context.Background(), &out, config.Overrides{}, "", true, true)
	var rep secreport.Report
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	if !rep.Sandbox.Requested {
		t.Fatalf("--sandbox was not seen as a request: %+v", rep.Sandbox)
	}
	if rep.Sandbox.State != "enabled" && rep.Sandbox.State != "requested-unavailable" {
		t.Fatalf("state = %q", rep.Sandbox.State)
	}
	out.Reset()
	_ = securityDoctorMode(context.Background(), &out, config.Overrides{}, "", false, true)
	rep = secreport.Report{}
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Sandbox.Requested || (rep.Sandbox.State != "disabled-by-choice" && rep.Sandbox.State != "unavailable") {
		t.Fatalf("no request but sandbox = %+v", rep.Sandbox)
	}
}

// /doctor security in a session is the same report, and sees a sandbox
// turned on with /sandbox after startup.
func TestSecuritySubjectInASessionSeesTheLiveSandbox(t *testing.T) {
	cwd, cfgDir, secrets := securityProject(t)
	cfg, err := config.Load(config.Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	gate, err := newTrustGate(cfgDir, "", os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	// A machine whose only backend is docker, so the test does not depend on this one's.
	sb := shell.ProbeSandbox(func(n string) (string, error) {
		if n == "docker" {
			return "/usr/bin/docker", nil
		}
		return "", os.ErrNotExist
	}, func() int { return 0 })
	reg := inspect.New()
	registerSecuritySubject(reg, securityEnv{cfg: cfg, cfgPath: filepath.Join(cfgDir, "config.json"), cfgDir: cfgDir, sandbox: sb,
		ext: extensionEnv{cfg: cfg, cfgPath: filepath.Join(cfgDir, "config.json"), cfgDir: cfgDir, cwd: func() string { return cwd }, gate: gate,
			ts: trust.Store{Path: filepath.Join(cfgDir, "mcp-trust.json")}}})

	req, ok := inspect.DoctorSubject("security json")
	if !ok || req != "security json" {
		t.Fatalf("DoctorSubject = %q %v", req, ok)
	}
	state := func() string {
		out, err := reg.Report(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range secrets {
			if strings.Contains(out, s) {
				t.Fatalf("printed %q", s)
			}
		}
		var rep secreport.Report
		if err := json.Unmarshal([]byte(out), &rep); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		return rep.Sandbox.State
	}
	if got := state(); got != "disabled-by-choice" && got != "unavailable" {
		t.Fatalf("before /sandbox: %q", got)
	}
	if err := sb.Enable(); err != nil {
		t.Skipf("this machine cannot enable the docker double: %v", err)
	}
	if got := state(); got != "enabled" {
		t.Fatalf("after /sandbox on: %q", got)
	}
	if _, err := reg.Report(context.Background(), "security yaml"); err == nil {
		t.Fatal("an unknown format was accepted")
	}
}

// --doctor with an unknown topic is a usage error, not the full checkup.
func TestDoctorUnknownTopicIsRejected(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	if err := fs.Parse([]string{"nonsense"}); err != nil {
		t.Fatal(err)
	}
	handled, err := earlyMode(fs, &options{runDoctor: true})
	if !handled || err == nil || !strings.Contains(err.Error(), "unknown --doctor topic") {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	if headless.ExitCode(err) != headless.ExitBadInput {
		t.Fatalf("exit code %d, want bad input", headless.ExitCode(err))
	}
}

// A fresh machine (CI, a clean container) has no config and no keys: the
// report still runs and says so rather than failing.
func TestDoctorSecurityWithNoConfigAtAll(t *testing.T) {
	_, cfgDir := extProject(t)
	if err := os.Remove(filepath.Join(cfgDir, "config.json")); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := securityDoctorMode(context.Background(), &out, config.Overrides{}, "", false, false); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "Sandbox") || !strings.Contains(out.String(), "no security check failed") {
		t.Fatalf("report:\n%s", out.String())
	}
}
