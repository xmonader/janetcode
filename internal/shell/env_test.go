package shell

import (
	"strings"
	"testing"
)

func TestScrubEnvLocale(t *testing.T) {
	get := func(env []string, key string) (string, bool) {
		val, ok := "", false
		for _, kv := range env { // last wins, as exec does
			if n, v, _ := strings.Cut(kv, "="); n == key {
				val, ok = v, true
			}
		}
		return val, ok
	}
	got := ScrubEnv([]string{"LC_ALL=C", "LC_CTYPE=en_US.UTF-8", "PATH=/bin"})
	if _, ok := get(got, "LC_ALL"); ok {
		t.Error("LC_ALL=C should be dropped; it defeats LC_CTYPE")
	}
	if v, _ := get(got, "LC_CTYPE"); v != "C.UTF-8" {
		t.Errorf("LC_CTYPE = %q, want C.UTF-8", v)
	}
	if v, _ := get(got, "PATH"); v != "/bin" {
		t.Errorf("PATH was disturbed: %q", v)
	}
	// A real locale is left exactly as the user set it.
	got = ScrubEnv([]string{"LC_ALL=de_DE.UTF-8", "LC_CTYPE=de_DE.UTF-8"})
	if v, ok := get(got, "LC_ALL"); !ok || v != "de_DE.UTF-8" {
		t.Errorf("a real LC_ALL must survive, got %q %v", v, ok)
	}
}

// The scrub named three variables: the two the config reads from the
// environment and one alias. The same config ships presets for sixteen
// other backends, whose keys a user exports the same way — and those
// went to every command the model ran.
func TestScrubEnvDropsEveryProviderKey(t *testing.T) {
	gone := []string{
		"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "OPENAI_API_KEY",
		"OPENROUTER_API_KEY", "DEEPSEEK_API_KEY", "GROQ_API_KEY",
		"MOONSHOT_API_KEY", "DASHSCOPE_API_KEY", "GEMINI_API_KEY",
		"XAI_API_KEY", "MISTRAL_API_KEY", "TOGETHER_API_KEY",
		"FIREWORKS_API_KEY", "CEREBRAS_API_KEY", "ZHIPU_API_KEY",
		"MINIMAX_API_KEY", "AWS_SECRET_ACCESS_KEY",
	}
	// Scrubbing by shape must not take what a command the model is
	// legitimately running needs: gh, npm publish and cargo all read one.
	kept := []string{"GITHUB_TOKEN", "NPM_TOKEN", "CARGO_REGISTRY_TOKEN", "AWS_ACCESS_KEY_ID", "PATH", "HOME"}

	var env []string
	for _, n := range append(append([]string{}, gone...), kept...) {
		env = append(env, n+"=v")
	}
	out := strings.Join(ScrubEnv(env), "\n")
	for _, n := range gone {
		if strings.Contains(out, n+"=") {
			t.Errorf("%s reached the command", n)
		}
	}
	for _, n := range kept {
		if !strings.Contains(out, n+"=") {
			t.Errorf("%s was taken from the command", n)
		}
	}
}
