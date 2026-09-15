package perm

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/xmonader/janetcode/internal/shell"
	"github.com/xmonader/janetcode/internal/xdg"
)

// Some files are not work. A shell startup file, a git hook, an .npmrc,
// a key under .ssh: writing one of them changes what happens the next
// time the user opens a terminal, commits, publishes or connects, long
// after this session ended and with nobody watching. The modes that make
// a session comfortable — /edits, /auto, a saved allow rule — exist so
// ordinary edits stop asking, and none of them was ever meant to cover
// these.
//
// So they are carved out the way janetcode's own control files already
// are (see tool.SelfPath): a write to one asks, whatever the mode, and
// the prompt says which path it is asking about. /yolo asks too. That is
// a deliberate choice rather than an oversight — see Protected.Reason.
//
// The set is data, so a project or a person can add to it:
// protected_paths in config.json is appended to these defaults, never
// replaces them.

// DefaultProtectedPaths are the paths protected everywhere. A pattern is
// matched with Match (see perm.Match), so * spans anything, including
// separators: "**/.ssh/**" is every .ssh directory anywhere. A leading ~
// is the user's home directory, and a pattern ending in /* or /** also
// matches the directory itself.
var DefaultProtectedPaths = []string{
	// Shell startup files and profiles: what the next terminal runs.
	"~/.bashrc", "~/.bash_profile", "~/.bash_login", "~/.bash_logout", "~/.profile",
	"~/.zshrc", "~/.zshenv", "~/.zprofile", "~/.zlogin", "~/.zlogout",
	"~/.kshrc", "~/.cshrc", "~/.tcshrc", "~/.inputrc",
	"~/.config/fish/**", "~/.config/zsh/**",
	"/etc/profile", "/etc/profile.d/**", "/etc/bash.bashrc", "/etc/zshenv", "/etc/zsh/**", "/etc/environment",
	// git: config decides what a commit does, hooks run on every commit.
	"**/.git/config", "**/.git/hooks/**", "~/.gitconfig", "~/.config/git/**",
	// Publishing and package credentials.
	"**/.npmrc", "**/.yarnrc", "**/.yarnrc.yml", "**/.pypirc", "**/.netrc", "**/_netrc",
	"~/.cargo/credentials", "~/.cargo/credentials.toml", "~/.docker/config.json",
	"~/.gem/credentials", "~/.config/gh/hosts.yml",
	// Keys and cloud credentials.
	"**/.ssh/**", "**/.aws/credentials", "**/.aws/config", "~/.gnupg/**", "~/.kube/config",
	// janetcode's own permission record. The rest of .janetcode/ and the
	// config directory are already guarded by tool.SelfPath; this one is
	// named here too so the set reads as what it is.
	"**/.janetcode/permissions.json",
	// Anything that runs at login or on a timer.
	"~/Library/LaunchAgents/**", "~/.config/systemd/user/**", "/etc/systemd/system/**", "/etc/cron.d/**", "~/.config/autostart/**",
}

// Protected is a set of protected path patterns: the defaults, the ones
// configuration added, and the two that can only be known at runtime —
// the trust store (which records what the user approved) and janetcode's
// own binary when it sits inside the workspace, since overwriting it is
// the shortest path from "may edit files here" to "may do anything".
//
// The zero Protected protects nothing, so a caller that never sets one
// up behaves exactly as before.
type Protected struct {
	patterns []string
}

// NewProtected builds the set: the defaults, then extra from
// configuration, then the runtime paths. extra is appended, so a
// configured list adds to the defaults and cannot take anything out.
func NewProtected(extra []string) *Protected {
	p := &Protected{}
	p.patterns = append(p.patterns, DefaultProtectedPaths...)
	for _, s := range extra {
		if s = strings.TrimSpace(s); s != "" {
			p.patterns = append(p.patterns, s)
		}
	}
	if dir, err := xdg.ConfigDir(os.Getenv); err == nil && dir != "" {
		p.patterns = append(p.patterns, filepath.Join(dir, "mcp-trust.json"))
	}
	if exe, err := os.Executable(); err == nil && exe != "" {
		if abs, err := filepath.Abs(exe); err == nil {
			p.patterns = append(p.patterns, abs)
		}
	}
	return p
}

// Patterns lists the set, for /permissions and the doctor.
func (p *Protected) Patterns() []string {
	if p == nil {
		return nil
	}
	return append([]string(nil), p.patterns...)
}

// MatchPath reports whether an absolute path is protected, and by which
// pattern.
func (p *Protected) MatchPath(abs string) (string, bool) {
	if p == nil || abs == "" {
		return "", false
	}
	abs = filepath.ToSlash(filepath.Clean(abs))
	for _, pat := range p.patterns {
		if matchProtected(pat, abs) {
			return pat, true
		}
	}
	return "", false
}

// matchProtected matches one pattern against a cleaned, slash-separated
// path. A pattern ending in /* or /** also matches the directory itself,
// so "~/.ssh/**" protects ~/.ssh as well as what is in it.
func matchProtected(pattern, abs string) bool {
	pattern = filepath.ToSlash(expandHome(strings.TrimSpace(pattern)))
	if pattern == "" {
		return false
	}
	if Match(pattern, abs) {
		return true
	}
	for _, suffix := range []string{"/**", "/*"} {
		if dir, ok := strings.CutSuffix(pattern, suffix); ok && Match(dir, abs) {
			return true
		}
	}
	return false
}

// expandHome turns a leading ~ or $HOME into the home directory.
func expandHome(s string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return s
	}
	switch {
	case s == "~":
		return home
	case strings.HasPrefix(s, "~/"):
		return home + s[1:]
	case strings.HasPrefix(s, "$HOME"):
		return home + strings.TrimPrefix(s, "$HOME")
	}
	return s
}

// Writes returns the first protected path a call would write to, with
// the pattern that protects it: the path itself for a file tool, and
// every target shell.WriteTargets finds for a command, so a redirect or
// a `cp` into a protected path is caught the same way an edit_file is.
func (p *Protected) Writes(c Call) (path, pattern string, ok bool) {
	if p == nil {
		return "", "", false
	}
	for _, t := range writeTargets(c) {
		if pat, hit := p.MatchPath(t); hit {
			return t, pat, true
		}
	}
	return "", "", false
}

// Reason is the line a prompt shows for a protected write, or "" when
// the call touches none. It is phrased as the warning it becomes:
// "protected path: ~/.zshenv".
//
// It is returned for /yolo too. A protected write is the one move whose
// consequences outlive the session — the next shell, the next commit,
// the next publish — and the set is small enough that a session doing
// ordinary work never meets it; the same reasoning already makes
// janetcode's own settings ask under every mode (see tool.SelfPath).
// Nothing else about --yolo changes.
func (p *Protected) Reason(c Call) string {
	path, _, ok := p.Writes(c)
	if !ok {
		return ""
	}
	return "protected path: " + ShortenHome(path)
}

// ShortenHome writes a path with the home directory as ~, the way the
// rest of the interface writes one.
func ShortenHome(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == home {
		return "~"
	}
	if rest, ok := strings.CutPrefix(path, home+string(filepath.Separator)); ok {
		return "~" + string(filepath.Separator) + rest
	}
	return path
}

// writeTargets are the absolute paths a call would write to.
func writeTargets(c Call) []string {
	switch c.Tool {
	case "bash":
		var in struct {
			Command string `json:"command"`
		}
		if json.Unmarshal(c.Input, &in) != nil || in.Command == "" {
			return nil
		}
		root := c.Root
		if root == "" {
			root, _ = os.Getwd()
		}
		return shell.WriteTargets(in.Command, root)
	case "write_file", "edit_file":
		var in struct {
			Path string `json:"path"`
		}
		if json.Unmarshal(c.Input, &in) != nil || strings.TrimSpace(in.Path) == "" {
			return nil
		}
		abs := in.Path
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(c.Root, abs)
		}
		return []string{filepath.Clean(abs)}
	}
	return nil
}

// HardDeny is the tier no rule reaches: a command that destroys the
// machine (shell.RiskCatastrophic) is refused whatever the allow rules
// and the modes say, and the reason is what the user and the model are
// told. The bash tool refuses these at run time as well; deciding it
// here means an allow rule never even prompts for one.
func HardDeny(c Call) string {
	if c.Tool != "bash" || len(c.Input) == 0 {
		return ""
	}
	var in struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(c.Input, &in) != nil || strings.TrimSpace(in.Command) == "" {
		return ""
	}
	if risk, why := shell.Classify(in.Command, c.Root); risk == shell.RiskCatastrophic {
		return why
	}
	return ""
}
