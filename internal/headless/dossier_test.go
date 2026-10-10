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
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

func dossierCall(id, name, input string) *llm.Response {
	return &llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
		{Type: llm.BlockText, Text: "Trying the cache fix."},
		{Type: llm.BlockToolUse, ID: id, Name: name, Input: json.RawMessage(input)}}}, StopReason: llm.StopToolUse}
}

// dossierRunOnce runs a headless turn that writes a file and runs check,
// then ends, and returns the result object and the session directory.
func dossierRunOnce(t *testing.T, check string, off map[string]bool) (CLIResult, string) {
	t.Helper()
	root, sess := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(root, "check.sh"), []byte(check), 0o644)
	f := &fake{responses: []*llm.Response{
		dossierCall("w1", "write_file", `{"path":"cache.go","content":"package cache\n"}`),
		dossierCall("b1", "bash", `{"command":"sh check.sh"}`),
		{Message: llm.TextMessage(llm.RoleAssistant, "Done."), StopReason: llm.StopEndTurn},
	}}
	ag := &agent.Agent{Provider: f, Tools: tool.NewRegistry(tool.Bash{Root: root}, tool.WriteFile{Root: root}), Model: "m", Root: root,
		AutoApprove: true, Log: &session.Memory{}, Off: off}
	ag.UndoPath = filepath.Join(sess, "s1.undo.json")
	var out, errOut bytes.Buffer
	_ = Run(context.Background(), Options{Agent: ag, Prompt: "fix cache.go eviction", Stdout: &out, Stderr: &errOut, Cwd: root, Format: FormatJSON})
	var res CLIResult
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("%v: %q (stderr %q)", err, out.String(), errOut.String())
	}
	return res, sess
}

func TestFailedRunLeavesADossier(t *testing.T) {
	res, sess := dossierRunOnce(t, "echo '--- FAIL: TestEvict (0.00s)'; echo FAIL; exit 1\n", nil)
	d := res.Dossier
	if d == nil {
		t.Fatalf("no dossier; verdict %q", res.Verdict)
	}
	if d.LastRed == nil || d.LastRed.Failing != 1 || d.Repro == nil || d.Repro.Command != "sh check.sh" {
		t.Fatalf("dossier %+v", d)
	}
	if len(d.Hypotheses) != 1 || d.Hypotheses[0].Verdict != "ruled out" || d.Hypotheses[0].Files[0] != "cache.go" || d.Hypotheses[0].Intent[0] != "Trying the cache fix." {
		t.Fatalf("hypotheses %+v", d.Hypotheses)
	}
	want := filepath.Join(sess, "s1.dossier.md")
	b, err := os.ReadFile(want)
	if err != nil || d.Path != want || !strings.Contains(string(b), "TestEvict") {
		t.Fatalf("dossier file %q (%v), path %q", b, err, d.Path)
	}
}

func TestPassingRunAndSwitchedOffLeaveNoDossier(t *testing.T) {
	if res, sess := dossierRunOnce(t, "echo 'ok  cache 0.01s'\n", nil); res.Dossier != nil {
		t.Fatalf("a green run got a dossier: %+v", res.Dossier)
	} else if _, err := os.Stat(filepath.Join(sess, "s1.dossier.md")); err == nil {
		t.Fatal("a green run wrote a dossier file")
	}
	if res, _ := dossierRunOnce(t, "echo '--- FAIL: TestEvict'; exit 1\n", map[string]bool{"dossier": true}); res.Dossier != nil {
		t.Fatal("JANETCODE_OFF=dossier still wrote one")
	}
}

// The scope report rides the same result object.
func TestResultCarriesTheScopeReport(t *testing.T) {
	res, _ := dossierRunOnce(t, "echo ok\n", nil)
	if res.Scope == nil || res.Scope.Edited != 1 || len(res.Scope.Outside) != 0 {
		t.Fatalf("scope %+v", res.Scope)
	}
}
