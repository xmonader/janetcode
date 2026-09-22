package shell

import "strings"

// secretEnv lists variables that model-run commands must never see: the
// agent's own credentials. Anything else the user exported stays available.
var secretEnv = map[string]bool{
	"ANTHROPIC_API_KEY":    true,
	"ANTHROPIC_AUTH_TOKEN": true,
	"OPENAI_API_KEY":       true,
}

// secretSuffix is the shape the rest of them are spelled in.
//
// The list above named three variables, which are the two the config
// reads from the environment plus one alias. The config also ships
// presets for sixteen other backends, and a user who picked one of those
// exports its key the same way — so OPENROUTER_API_KEY, DEEPSEEK_API_KEY,
// GROQ_API_KEY and the rest went to every command the model ran, and a
// single `env` or a curl to somewhere else was enough.
//
// Matching by suffix rather than by a longer list is what makes it hold
// for the next backend nobody has added yet. It is deliberately narrower
// than "anything that looks like a credential": GITHUB_TOKEN, NPM_TOKEN
// and CARGO_REGISTRY_TOKEN are what `gh`, `npm publish` and `cargo` need
// from a command the model is legitimately running, and they do not match.
// An MCP server that needs one of these is unaffected — its configured
// env is expanded from the real environment and appended after the scrub.
// Matching only at the end of the name was the gap a recorded eval run
// walked through: a person who holds two DeepSeek keys spells the second
// one DEEPSEEK_API_KEY_PERSONAL, which ends in _PERSONAL, so it went to
// every command the model ran and an `env` printed it. The phrase is
// what says credential, and it says it wherever in the name it sits — so
// the match is on whole `_`-separated words anywhere, which is why
// MONKEY and KEYBOARD_LAYOUT are untouched.
var secretPhrase = []string{"_API_KEY_", "_AUTH_TOKEN_", "_API_TOKEN_", "_SECRET_KEY_", "_ACCESS_KEY_"}

// isSecretName reports whether a variable holds a credential this process
// must not hand to a model-run command.
func isSecretName(name string) bool {
	if secretEnv[name] {
		return true
	}
	upper := strings.ToUpper(name)
	// AWS_ACCESS_KEY_ID names the public half of a pair; the secret half
	// is AWS_SECRET_ACCESS_KEY, and aws needs both to be told apart. An
	// identifier is not a credential whatever precedes it.
	if strings.HasSuffix(upper, "_ID") {
		return false
	}
	padded := "_" + upper + "_"
	for _, p := range secretPhrase {
		if strings.Contains(padded, p) {
			return true
		}
	}
	return false
}

// nonInteractiveEnv stops common tools from waiting on a human: no pagers,
// no editors, no credential prompts. Commands get EOF on stdin regardless.
var nonInteractiveEnv = []string{
	"PAGER=cat",
	"GIT_PAGER=cat",
	"GIT_EDITOR=true",
	"GIT_TERMINAL_PROMPT=0",
	"DEBIAN_FRONTEND=noninteractive",
	"CI=true",
	"NO_COLOR=1",
	"NONINTERACTIVE=1",
	// less pages even a short file unless told not to, and -X keeps it
	// from clearing the screen on exit.
	"LESS=-FX",
}

// nonUTF8Locales are the C/POSIX locales. LC_ALL overrides every LC_*
// category, so a user or CI image that exports LC_ALL=C makes tools emit
// non-UTF-8 bytes no matter what LC_CTYPE says — and the agent then reads
// mangled output. Only that case is touched; a real locale is left alone.
var nonUTF8Locales = map[string]bool{"C": true, "POSIX": true}

// ScrubEnv is the environment for a model-run command: env without the
// agent's credentials, with a C/POSIX LC_ALL replaced by a UTF-8 LC_CTYPE,
// and with the non-interactive settings forced.
func ScrubEnv(env []string) []string {
	out := make([]string, 0, len(env)+len(nonInteractiveEnv))
	forced := map[string]bool{}
	for _, kv := range nonInteractiveEnv {
		name, _, _ := strings.Cut(kv, "=")
		forced[name] = true
	}
	dropLCAll := false
	for _, kv := range env {
		if name, val, _ := strings.Cut(kv, "="); name == "LC_ALL" && nonUTF8Locales[val] {
			dropLCAll = true
		}
	}
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if isSecretName(name) || forced[name] {
			continue
		}
		if dropLCAll && (name == "LC_ALL" || name == "LC_CTYPE") {
			continue
		}
		out = append(out, kv)
	}
	if dropLCAll {
		out = append(out, "LC_CTYPE=C.UTF-8")
	}
	return append(out, nonInteractiveEnv...)
}
