package tool

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// Everything that decides what janetcode may do without asking — the
// config file, the project's MCP server list, the hooks, permission rules,
// verification gates, rules, skills and commands under .janetcode/, and
// the memory the agent carries between sessions — is a file. A model that
// can write files can therefore widen its own permissions and then use
// them, and --yolo, auto-edit mode and a saved allow rule are exactly the
// settings that would let it do so unobserved.
//
// So those paths are carved out: a write, an edit or a shell command that
// touches one asks, whatever the mode. This is defence in depth rather
// than a boundary — a determined model has a hundred ways to write a file
// — but it means the one move that turns a limited session into an
// unlimited one is the one move that is always shown to a human.

// SelfPath names the control surface an absolute path belongs to, or "".
func SelfPath(root, abs string) string {
	abs = filepath.Clean(abs)
	if root != "" {
		root = filepath.Clean(root)
		if abs == filepath.Join(root, ".mcp.json") {
			return "the project's MCP server list"
		}
		if under(abs, filepath.Join(root, ".janetcode")) {
			return "janetcode's project settings (hooks, permission rules, verification, rules, skills and commands)"
		}
	}
	if d := janetConfigDir(); d != "" && under(abs, d) {
		return "janetcode's own configuration"
	}
	if d := janetDataDir(); d != "" && under(abs, d) {
		return "janetcode's sessions and memory store"
	}
	return ""
}

// under reports whether abs is dir or lies inside it.
func under(abs, dir string) bool {
	return abs == dir || strings.HasPrefix(abs, dir+string(filepath.Separator))
}

// janetConfigDir mirrors config.Path's rule without importing it: that
// package already depends on this one.
func janetConfigDir() string {
	if base := os.Getenv("XDG_CONFIG_HOME"); base != "" {
		return filepath.Join(base, "janetcode")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "janetcode")
}

// janetDataDir mirrors session.Dir's rule; the memory store lives beside
// the sessions under it.
func janetDataDir() string {
	if base := os.Getenv("XDG_DATA_HOME"); base != "" {
		return filepath.Join(base, "janetcode")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "share", "janetcode")
}

// selfPathToken resolves one shell token the way escapes does and asks
// whether it names a control surface.
func selfPathToken(tk, root string) string {
	tk = strings.Trim(tk, `"'`)
	if tk == "" || strings.HasPrefix(tk, "-") {
		return ""
	}
	if strings.HasPrefix(tk, "~") || strings.HasPrefix(tk, "$HOME") {
		if home, err := os.UserHomeDir(); err == nil {
			if strings.HasPrefix(tk, "~") {
				tk = home + tk[1:]
			} else {
				tk = home + strings.TrimPrefix(tk, "$HOME")
			}
		}
	}
	// A glob cannot be judged, but the directory it sits in can: writing
	// into .janetcode/* is the same move as writing into .janetcode/x.
	tk = strings.TrimSuffix(strings.TrimSuffix(tk, "*"), "/")
	if tk == "" {
		return ""
	}
	abs := tk
	if !filepath.IsAbs(tk) {
		if root == "" {
			return ""
		}
		abs = filepath.Join(root, tk)
	}
	return SelfPath(root, abs)
}

// selfModifyingCommand reports the first control surface a command line
// would write to, and how. Only the obvious shell forms are recognised —
// a redirect, and the file-writing commands with a path argument.
func selfModifyingCommand(command, root string) string {
	for _, seg := range splitSegments(command) {
		toks := shellWords(seg)
		if len(toks) == 0 {
			continue
		}
		name := leadingCommand(toks, 0)
		if writesItsArguments[name] {
			for _, tk := range toks[1:] {
				if what := selfPathToken(tk, root); what != "" {
					return name + " would change " + what
				}
			}
		}
		// dd of=… and tee's arguments are covered above; a redirect can
		// point anywhere regardless of the command in front of it.
		for _, m := range redirect.FindAllStringSubmatch(seg, -1) {
			if what := selfPathToken(m[1], root); what != "" {
				return "this writes to " + what
			}
		}
	}
	return ""
}

// writesItsArguments lists commands whose path arguments they overwrite.
// sed and perl are here only because of -i, which is checked by the
// command having been named at all: a read-only sed on a config file is
// harmless, and asking about it costs one keypress.
var writesItsArguments = map[string]bool{
	"cp": true, "mv": true, "rm": true, "tee": true, "ln": true, "install": true,
	"truncate": true, "shred": true, "chmod": true, "chown": true, "rsync": true,
	"sed": true, "perl": true, "patch": true, "dd": true, "mkdir": true, "touch": true,
	"python": true, "python3": true,
}

// guardSelfModify is the Guard body shared by write_file and edit_file.
func guardSelfModify(root string, input json.RawMessage) (bool, string) {
	var in struct {
		Path string `json:"path"`
	}
	if json.Unmarshal(input, &in) != nil || strings.TrimSpace(in.Path) == "" {
		return false, ""
	}
	abs := in.Path
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(root, abs)
	}
	if what := SelfPath(root, abs); what != "" {
		return true, "this changes " + what + ", which decides what janetcode may do without asking"
	}
	return false, ""
}
