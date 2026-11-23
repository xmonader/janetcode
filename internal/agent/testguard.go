package agent

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/xmonader/janetcode/internal/llm"
)

// A test guard refuses a headless run's first write to a test file, so
// the model keeps its own checks inline and leaves the repository's tests
// as it found them. The headless prompt already says so, and MiMo still
// rewrote test_public.py (effbench r9, b-invoice-totals) and wrote and
// then loosened a test_cronx.py of its own (r11, d-cronexpr): either is a
// tamper flag on a run that would otherwise pass. A second write to the
// same path goes through, for a task that does ask for tests.
// JANETCODE_OFF=testguard turns it off.

// asksForTestsRe matches a request that asks for tests to be written or
// changed. Such a request is not guarded at all: every realrepo task says
// "add tests", and the refusal only cost each run a model round before
// the same call went through.
var asksForTestsRe = regexp.MustCompile(`(?i)\b(add|adding|write|writing|extend|update|include)\b[^.\n]{0,40}\b(tests?|test cases?|regression)\b|\bregression tests?\b`)

// testGuardNote is the refusal; the path is filled in.
const testGuardNote = "%s is a test file. This run leaves the repository's tests unchanged and keeps checks of its own in an inline command (python - <<'EOF' ... EOF, node -e, go run): run your check that way instead. If the task itself asks you to add or change this test file, make the same call again and it will go through."

// testFileDenial is the refusal for a guarded write, or "".
func (a *Agent) testFileDenial(u llm.Block) string {
	if !a.TestGuard || a.Off["testguard"] || (u.Name != "write_file" && u.Name != "edit_file" && u.Name != "ast_edit" && u.Name != "apply_patch") {
		return ""
	}
	var in struct {
		Path string `json:"path"`
	}
	if json.Unmarshal(u.Input, &in) != nil || !isTestFile(in.Path) || asksForTestsRe.MatchString(a.integ.userText) {
		return ""
	}
	p := filepath.Clean(in.Path)
	if a.testGuarded[p] {
		return ""
	}
	if a.testGuarded == nil {
		a.testGuarded = map[string]bool{}
	}
	a.testGuarded[p] = true
	return strings.Replace(testGuardNote, "%s", in.Path, 1)
}
