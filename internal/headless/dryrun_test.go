package headless

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/tool"
)

// dryRunAgent is an agent in a dry run whose preview holds one edit of
// a.txt, made the way write_file makes it: through the workspace.
func dryRunAgent(t *testing.T) *agent.Agent {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "a.txt")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws := tool.NewLocal(root)
	ag := &agent.Agent{Tools: tool.NewRegistry(tool.WriteFile{WS: ws})}
	ag.SetDryRun(true)
	if err := ws.Host().WriteFile(context.Background(), path, []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return ag
}

// TestJSONResultCarriesTheDryRunPreview: a headless dry run's product is
// its preview, and a script reading the result object gets all of it.
func TestJSONResultCarriesTheDryRunPreview(t *testing.T) {
	ag := dryRunAgent(t)
	var out strings.Builder
	r := newRecorder(Options{Stdout: &out}, FormatJSON)
	r.finish(ag, nil)
	var res CLIResult
	if err := json.Unmarshal([]byte(out.String()), &res); err != nil {
		t.Fatal(err)
	}
	if res.DryRun == nil || len(res.DryRun.Files) != 1 {
		t.Fatalf("dry_run = %+v", res.DryRun)
	}
	if f := res.DryRun.Files[0]; !strings.Contains(f.Diff, "-old") || !strings.Contains(f.Diff, "+new") {
		t.Fatalf("file diff = %q", f.Diff)
	}
}

// TestTextDryRunPrintsThePreview: in text mode the preview goes to
// stderr, diffs included.
func TestTextDryRunPrintsThePreview(t *testing.T) {
	ag := dryRunAgent(t)
	var out, errOut strings.Builder
	r := newRecorder(Options{Stdout: &out, Stderr: &errOut}, FormatText)
	r.finish(ag, nil)
	if !strings.Contains(errOut.String(), "files that would change (1)") || !strings.Contains(errOut.String(), "+new") {
		t.Fatalf("stderr = %q", errOut.String())
	}
}
