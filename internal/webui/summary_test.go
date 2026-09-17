package webui

import (
	"strings"
	"testing"
)

// An MCP tool is third-party code, and its argument values often carry a
// credential the model copied out of context. None of the keys the
// summary looks for are theirs, so it fell through to printing the whole
// raw JSON: the summary of an MCP call was its arguments, verbatim, in
// the browser and in the SSE stream behind it. The terminal front-end
// names the keys only; this one now does the same.
func TestSummarizeInputHidesMCPValues(t *testing.T) {
	secret := "sk-secret-token-value"
	raw := `{"repo":"a/b","api_key":"` + secret + `","dry_run":true}`

	got := summarizeInput("mcp__deploy__ship", raw, 200, "")
	if strings.Contains(got, secret) {
		t.Fatalf("a value reached the summary: %q", got)
	}
	if got != "{api_key, dry_run, repo}" {
		t.Fatalf("got %q", got)
	}
	// A built-in tool is the harness's own code and is summarised as before.
	if got := summarizeInput("bash", `{"command":"go test ./..."}`, 200, ""); got != "go test ./..." {
		t.Fatalf("a built-in summary changed: %q", got)
	}
}
