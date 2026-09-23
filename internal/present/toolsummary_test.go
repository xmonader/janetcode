package present

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// One tool call reads the same in the terminal and the browser. These are
// the rules each front-end had on its own; the web lacked most of the
// terminal's and the terminal lacked the plan summary.
func TestToolSummary(t *testing.T) {
	home, _ := os.UserHomeDir()
	cases := []struct{ name, raw, cwd, want string }{
		{"read_file", `{"path":"/work/proj/internal/x/x.go","offset":120}`, "/work/proj", "internal/x/x.go from line 120"},
		{"grep", `{"pattern":"foo","path":"/work/proj/src"}`, "/work/proj", "foo in src"},
		{"bash", `{"command":"cd /work/proj && go test ./..."}`, "/work/proj", "go test ./..."},
		{"bash", `{"command":"cd . && make"}`, "/work/proj", "make"},
		{"bash", `{"command":"cd /elsewhere && make"}`, "/work/proj", "cd /elsewhere && make"},
		{"bash", `{"command":"seq 1 400 > /work/proj/out.txt"}`, "/work/proj", "seq 1 400 > out.txt"},
		{"bash", `{"command":"a\nb"}`, "", "a⏎ b"},
		{"todo", `{"todos":[{"text":"Read","status":"done"},{"text":"Write tests","status":"in_progress"},{"text":"Ship","status":"pending"}]}`, "", "3 steps · Write tests"},
	}
	if home != "" {
		cases = append(cases, struct{ name, raw, cwd, want string }{"read_file", `{"path":"` + filepath.Join(home, "notes.md") + `"}`, "/work/proj", "~/notes.md"})
	}
	for _, c := range cases {
		if got := ToolSummary(c.name, c.raw, 80, c.cwd); got != c.want {
			t.Errorf("%s %s → %q, want %q", c.name, c.raw, got, c.want)
		}
	}
}

// An MCP call's values are never shown, whatever shape its arguments have.
func TestToolSummaryHidesMCPValues(t *testing.T) {
	const secret = "sk-live-abc123"
	for raw, want := range map[string]string{
		`{"api_key":"` + secret + `","repo":"x"}`: "{api_key, repo}",
		`"` + secret + `"`:                        "(json string)",
		`["` + secret + `"]`:                      "(json array)",
		secret + " not json":                      "(arguments are not valid JSON)",
	} {
		if got := ToolSummary("mcp__github__call", raw, 200, ""); got != want || strings.Contains(got, secret) {
			t.Errorf("%s → %q, want %q", raw, got, want)
		}
	}
}

func TestClipCountsColumnsNotBytes(t *testing.T) {
	if got := Clip(strings.Repeat("界", 20), 10); got != strings.Repeat("界", 4)+"…" {
		t.Fatalf("wide runes: %q", got)
	}
	if got := Clip(strings.Repeat("v", 50), 20); len([]rune(got)) != 20 || !strings.HasSuffix(got, "…") {
		t.Fatalf("clip = %q", got)
	}
}
