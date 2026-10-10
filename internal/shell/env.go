package shell

import (
	"strings"
	"sync"
)

// secretEnv lists variables that model-run commands must never see: the
// agent's own credentials. Anything else the user exported stays available.
var secretEnv = map[string]bool{
	"ANTHROPIC_API_KEY":    true,
	"ANTHROPIC_AUTH_TOKEN": true,
	"OPENAI_API_KEY":       true,
	// The web UI's access token. Whoever holds it can post to
	// /api/permission, so a command that read it could answer its own
	// permission prompt — and every later one — with a curl to loopback.
	"JANETCODE_WEB_TOKEN": true,
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
		// OLDPWD is the directory janetcode was started from, which is
		// not the workspace: "cd $OLDPWD" took a model's command out of
		// the project in one step (hero_agent's effbench caught one
		// running pytest over a whole checkout that way).
		if isSecretName(name) || forced[name] || name == "OLDPWD" {
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
	out = append(out, nonInteractiveEnv...)
	// The honeytokens go last and only under names the user has not
	// set: a real variable of the same name is never shadowed.
	for _, kv := range Planted() {
		name, _, _ := strings.Cut(kv, "=")
		if !hasName(env, name) {
			out = append(out, kv)
		}
	}
	return out
}

func hasName(env []string, name string) bool {
	for _, kv := range env {
		if n, _, _ := strings.Cut(kv, "="); n == name {
			return true
		}
	}
	return false
}

// planted are the honeytoken variables (see egress.Canary) every
// model-run command gets. They live here, process-wide, because every
// backend that starts a command — the local shell, bwrap, Landlock,
// seatbelt — builds its environment through ScrubEnv.
var (
	plantedMu sync.Mutex
	planted   []string
)

// Plant sets the honeytoken variables, NAME=value each; nil removes them.
func Plant(kv []string) {
	plantedMu.Lock()
	defer plantedMu.Unlock()
	planted = append([]string(nil), kv...)
}

// Planted is what Plant set.
func Planted() []string {
	plantedMu.Lock()
	defer plantedMu.Unlock()
	return append([]string(nil), planted...)
}

// credentialWords are what a variable a command keeps is named for when
// it holds a credential: the ones ScrubEnv leaves in on purpose
// (GITHUB_TOKEN for gh, NPM_TOKEN for npm publish) are still secrets a
// command can read.
var credentialWords = []string{"TOKEN", "SECRET", "PASSWORD", "PASSWD", "CREDENTIAL", "PRIVATE_KEY", "API_KEY", "ACCESS_KEY"}

// ExposedSecrets names the variables in env that a model-run command
// would still see and that look like credentials, the honeytokens left
// out. It is one leg of the lethal trifecta: secrets a command can read.
func ExposedSecrets(env []string) []string {
	plantedNames := map[string]bool{}
	for _, kv := range Planted() {
		n, _, _ := strings.Cut(kv, "=")
		plantedNames[n] = true
	}
	var out []string
	for _, kv := range ScrubEnv(env) {
		name, val, _ := strings.Cut(kv, "=")
		if val == "" || plantedNames[name] {
			continue
		}
		upper := strings.ToUpper(name)
		for _, w := range credentialWords {
			if strings.Contains(upper, w) {
				out = append(out, name)
				break
			}
		}
	}
	return out
}
