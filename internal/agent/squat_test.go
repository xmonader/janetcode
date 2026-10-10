package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

// squatRun writes app.py with content through write_file and returns the
// tool result the model saw and the notices.
func squatRun(t *testing.T, content string, off bool) (string, []string) {
	t.Helper()
	in := `{"path":"app.py","content":` + squatJSON(content) + `}`
	f := &fake{responses: []*llm.Response{toolCall("w1", "write_file", in), text("done")}}
	a, _ := newAgent(t, f, nil)
	a.AutoApprove = true
	if off {
		a.Off = map[string]bool{"typosquat": true}
	}
	evs, emit := collect()
	if err := a.Run(context.Background(), "go", emit); err != nil {
		t.Fatal(err)
	}
	var result string
	var notices []string
	for _, e := range *evs {
		switch {
		case e.Kind == EvToolResult:
			result = e.Output
		case e.Kind == EvNotice && strings.HasPrefix(e.Text, "[typosquat check]"):
			notices = append(notices, e.Text)
		}
	}
	return result, notices
}

func squatJSON(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return `"` + r.Replace(s) + `"`
}

func TestSquatFlagsANearMissImport(t *testing.T) {
	out, notices := squatRun(t, "import os\nimport reqeusts\n", false)
	if !strings.Contains(out, "reqeusts is one keystroke from requests") || len(notices) != 1 {
		t.Fatalf("result %q\nnotices %q", out, notices)
	}
}

func TestSquatQuietOnRealImportsAndWhenOff(t *testing.T) {
	if out, notices := squatRun(t, "import os\nimport requests\nfrom yaml import safe_load\n", false); strings.Contains(out, "typosquat") || len(notices) != 0 {
		t.Fatalf("real imports flagged: %q %q", out, notices)
	}
	if out, notices := squatRun(t, "import reqeusts\n", true); strings.Contains(out, "typosquat") || len(notices) != 0 {
		t.Fatalf("flagged with the switch off: %q %q", out, notices)
	}
}
