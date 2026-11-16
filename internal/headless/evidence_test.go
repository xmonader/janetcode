package headless

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/evidence"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

// The JSON result carries the evidence model next to the verdict: the
// exit code and the verdict text stay what they were (backward
// compatible), and the new object says what each check was and whether it
// still backs a success claim.
func TestResultCarriesEvidenceAndKeepsTheVerdictAndExitCode(t *testing.T) {
	res, _ := dossierRunOnce(t, "echo ok  pkg 0.1s\n", nil)
	ev := res.Evidence
	if ev == nil || ev.Status != evidence.Passed || ev.Blocks || len(ev.Checks) != 1 {
		t.Fatalf("evidence = %+v", ev)
	}
	c := ev.Checks[0]
	if c.Command != "sh check.sh" || c.Exit != 0 || !c.Fresh || c.Source != "bash" || c.Summary == "" {
		t.Errorf("check = %+v", c)
	}
	if res.ExitCode != 0 || res.IsError || !strings.Contains(res.Verdict, "verified") {
		t.Errorf("exit %d is_error %v verdict %q must be as before", res.ExitCode, res.IsError, res.Verdict)
	}

	red, _ := dossierRunOnce(t, "echo '--- FAIL: TestEvict (0.00s)'; echo FAIL; exit 1\n", nil)
	if red.Evidence == nil || red.Evidence.Status != evidence.Failed || !red.Evidence.Blocks || red.Evidence.Checks[0].Exit != 1 {
		t.Fatalf("red evidence = %+v", red.Evidence)
	}
}

// A check that ran before the latest edit is stale in the result, and the
// stream's verdict event carries the same object.
func TestStaleCheckIsStaleInTheResultAndTheStream(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "check.sh"), []byte("echo ok  pkg 0.1s\n"), 0o644)
	f := &fake{responses: []*llm.Response{
		dossierCall("w1", "write_file", `{"path":"cache.go","content":"package cache\n"}`),
		dossierCall("b1", "bash", `{"command":"sh check.sh"}`),
		dossierCall("w2", "write_file", `{"path":"cache.go","content":"package cache\n\nvar X = 1\n"}`),
		{Message: llm.TextMessage(llm.RoleAssistant, "Done."), StopReason: llm.StopEndTurn},
	}}
	ag := &agent.Agent{Provider: f, Tools: tool.NewRegistry(tool.Bash{Root: root}, tool.WriteFile{Root: root}), Model: "m", Root: root,
		AutoApprove: true, Log: &session.Memory{}}
	var out, errOut bytes.Buffer
	_ = Run(context.Background(), Options{Agent: ag, Prompt: "edit cache.go", Stdout: &out, Stderr: &errOut, Cwd: root, Format: FormatStreamJSON})
	var res CLIResult
	var verdict *streamEvent
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var probe struct {
			Type string `json:"type"`
		}
		if json.Unmarshal([]byte(line), &probe) != nil {
			continue
		}
		switch probe.Type {
		case CLIResultType:
			if err := json.Unmarshal([]byte(line), &res); err != nil {
				t.Fatal(err)
			}
		case "verdict":
			var se streamEvent
			if err := json.Unmarshal([]byte(line), &se); err != nil {
				t.Fatal(err)
			}
			verdict = &se
		}
	}
	ev := res.Evidence
	if ev == nil || ev.Status != evidence.NotRun || !ev.Stale || !ev.Blocks || len(ev.Checks) != 1 || ev.Checks[0].Fresh || ev.Checks[0].StaleBy == "" {
		t.Fatalf("result evidence = %+v", ev)
	}
	if verdict == nil || verdict.Evidence == nil || verdict.Evidence.Status != evidence.NotRun {
		t.Fatalf("verdict event = %+v", verdict)
	}
}

// Text mode prints the evidence line on stderr beside the verdict, never
// on stdout, where only the answer goes.
func TestTextModePrintsEvidenceBesideTheVerdictOnStderr(t *testing.T) {
	root := t.TempDir()
	f := &fake{responses: []*llm.Response{
		dossierCall("w1", "write_file", `{"path":"cache.go","content":"package cache\n"}`),
		{Message: llm.TextMessage(llm.RoleAssistant, "Done."), StopReason: llm.StopEndTurn},
	}}
	ag := &agent.Agent{Provider: f, Tools: tool.NewRegistry(tool.WriteFile{Root: root}), Model: "m", Root: root,
		AutoApprove: true, Log: &session.Memory{}}
	var out, errOut bytes.Buffer
	if err := Run(context.Background(), Options{Agent: ag, Prompt: "write", Stdout: &out, Stderr: &errOut, Cwd: root}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut.String(), "evidence: not_run") {
		t.Fatalf("stderr = %q", errOut.String())
	}
	if strings.Contains(out.String(), "evidence") {
		t.Fatalf("stdout = %q", out.String())
	}
}
