package shell

import "strings"

// secretEnv lists variables that model-run commands must never see: the
// agent's own credentials. Anything else the user exported stays available.
var secretEnv = map[string]bool{
	"ANTHROPIC_API_KEY":    true,
	"ANTHROPIC_AUTH_TOKEN": true,
	"OPENAI_API_KEY":       true,
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
		if secretEnv[name] || forced[name] {
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
