package agent

import (
	"strings"
	"testing"
)

// The language setting reaches the model as a system-prompt paragraph,
// and changing it mid-session changes the next request's prompt.
func TestLanguageIsInTheSystemPrompt(t *testing.T) {
	a := &Agent{System: "sys"}
	if got := a.systemPrompt(); strings.Contains(got, "# Language") {
		t.Fatalf("no language set, yet the prompt has a language section:\n%s", got)
	}
	a.SetLanguage(" Spanish ")
	got := a.systemPrompt()
	if !strings.Contains(got, "# Language") || !strings.Contains(got, "in Spanish,") {
		t.Fatalf("prompt does not carry the language:\n%s", got)
	}
	// Identifiers and commit messages are the project's, not the user's.
	if !strings.Contains(got, "commit messages and file contents stay in the language the project already uses") {
		t.Errorf("prompt does not keep code in the project's language:\n%s", got)
	}
	a.SetLanguage("")
	if strings.Contains(a.systemPrompt(), "# Language") {
		t.Error("clearing the language left the paragraph in")
	}
}
