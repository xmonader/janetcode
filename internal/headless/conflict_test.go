package headless

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

// A run whose turn ends on a confirmed CONFLICT exits 58, and its result
// object carries the claim with is_error false: a terminal state, not a
// failure.
func TestConflictExitsWithItsOwnStatus(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "tests"), 0o755)
	os.WriteFile(filepath.Join(root, "tests", "check.sh"), []byte(`[ "$(cat total)" = "42" ] || { echo "FAIL test_total"; exit 1; }`+"\n"), 0o644)
	os.WriteFile(filepath.Join(root, "total"), []byte("40\n"), 0o644)
	f := &fake{responses: []*llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ID: "t1",
			Name: "bash", Input: json.RawMessage(`{"command":"sh tests/check.sh"}`)}}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "CONFLICT: test_total | the total is 40 | the test wants 42"), StopReason: llm.StopEndTurn},
	}}
	ag := &agent.Agent{Provider: f, Tools: tool.NewRegistry(tool.Bash{Root: root}), Model: "m", Root: root,
		AutoApprove: true, Log: &session.Memory{}, ConflictExit: true}
	var out, errOut bytes.Buffer
	err := Run(context.Background(), Options{Agent: ag, Prompt: "fix it", Stdout: &out, Stderr: &errOut, Cwd: root, Format: FormatJSON, Until: "sh tests/check.sh"})
	if !errors.Is(err, ErrConflict) || ExitCode(err) != ExitConflict {
		t.Fatalf("err = %v (exit %d), stderr %q", err, ExitCode(err), errOut.String())
	}
	var res CLIResult
	if jerr := json.Unmarshal(out.Bytes(), &res); jerr != nil {
		t.Fatalf("%v: %q", jerr, out.String())
	}
	if res.IsError || res.ExitCode != ExitConflict || res.Conflict == nil || !res.Conflict.Verified || res.Conflict.Test != "test_total" {
		t.Fatalf("result = %+v conflict %+v", res, res.Conflict)
	}
	// --until did not start another round on the test the model says
	// cannot pass.
	if f.calls != 2 {
		t.Fatalf("%d model calls, want 2", f.calls)
	}
}
