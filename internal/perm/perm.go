// Package perm holds persistent permission rules: which tool calls run
// without asking and which are refused outright. A rule is "tool" or
// "tool(pattern)", where the pattern is matched against the call's
// subject: the command for bash, the path for file tools, the URL for
// fetch_url. Deny rules win over allow rules; anything unmatched asks.
package perm

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	tools "github.com/xmonader/janetcode/internal/tool"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Rule is one allow or deny entry.
type Rule struct {
	Tool    string
	Pattern string // "" matches every call of the tool
}

// Parse reads "tool" or "tool(pattern)".
func Parse(s string) (Rule, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Rule{}, errors.New("empty rule")
	}
	i := strings.IndexByte(s, '(')
	if i < 0 {
		if strings.ContainsAny(s, " )") {
			return Rule{}, fmt.Errorf("bad rule %q: want tool or tool(pattern)", s)
		}
		return Rule{Tool: s}, nil
	}
	if !strings.HasSuffix(s, ")") || i == 0 {
		return Rule{}, fmt.Errorf("bad rule %q: want tool(pattern)", s)
	}
	return Rule{Tool: strings.TrimSpace(s[:i]), Pattern: strings.TrimSpace(s[i+1 : len(s)-1])}, nil
}

// IsZero reports whether r is no rule at all: what Suggest returns for a
// call it cannot capture safely.
func (r Rule) IsZero() bool { return r.Tool == "" }

func (r Rule) String() string {
	if r.Pattern == "" {
		return r.Tool
	}
	return r.Tool + "(" + r.Pattern + ")"
}

// Verdict is what the rules say about a call.
type Verdict int

const (
	Ask Verdict = iota
	Allowed
	Denied
)

func (v Verdict) String() string { return [...]string{"ask", "allow", "deny"}[v] }

// Rules is a set of allow and deny rules.
type Rules struct {
	Allow []Rule
	Deny  []Rule
}

// ParseAll parses rule strings, stopping at the first bad one.
func ParseAll(ss []string) ([]Rule, error) {
	var out []Rule
	for _, s := range ss {
		r, err := Parse(s)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// Decide matches a call. Deny wins; the matching rule is returned.
func (rs Rules) Decide(tool, subject string) (Verdict, Rule) {
	for _, r := range rs.Deny {
		if r.matches(tool, subject) {
			return Denied, r
		}
	}
	for _, r := range rs.Allow {
		if r.matches(tool, subject) {
			return Allowed, r
		}
	}
	return Ask, Rule{}
}

// Add appends a rule unless an equal one exists.
func (rs *Rules) Add(v Verdict, r Rule) bool {
	list := &rs.Allow
	if v == Denied {
		list = &rs.Deny
	}
	for _, e := range *list {
		if e == r {
			return false
		}
	}
	*list = append(*list, r)
	return true
}

// hasAllow reports whether an equal rule is already saved as an allow.
func (rs Rules) hasAllow(r Rule) bool {
	for _, e := range rs.Allow {
		if e == r {
			return true
		}
	}
	return false
}

// Merge returns the union of both sets, a's rules first.
func Merge(a, b Rules) Rules {
	out := Rules{}
	for _, r := range append(append([]Rule{}, a.Allow...), b.Allow...) {
		out.Add(Allowed, r)
	}
	for _, r := range append(append([]Rule{}, a.Deny...), b.Deny...) {
		out.Add(Denied, r)
	}
	return out
}

func (r Rule) matches(tool, subject string) bool {
	if r.Tool != tool && r.Tool != "*" {
		return false
	}
	return r.Pattern == "" || Match(r.Pattern, subject)
}

// Match is a wildcard match where * spans anything (including / and
// spaces) and ? one character. A pattern without wildcards must equal the
// subject; commands are compared after whitespace normalisation.
//
// * spans shell operators too: `bash(go test *)` matches
// `go test ./... && rm -rf y`. A rule is a prefix the user trusts, not a
// parse of the command, which is why Suggest never proposes one for a
// command whose leading words do not decide everything that runs.
func Match(pattern, subject string) bool {
	pattern = strings.Join(strings.Fields(pattern), " ")
	subject = strings.Join(strings.Fields(subject), " ")
	return glob(pattern, subject)
}

func glob(p, s string) bool {
	for len(p) > 0 {
		switch p[0] {
		case '*':
			p = strings.TrimLeft(p, "*")
			if p == "" {
				return true
			}
			for i := 0; i <= len(s); i++ {
				if glob(p, s[i:]) {
					return true
				}
			}
			return false
		case '?':
			if s == "" {
				return false
			}
			p, s = p[1:], s[1:]
		default:
			if s == "" || p[0] != s[0] {
				return false
			}
			p, s = p[1:], s[1:]
		}
	}
	return s == ""
}

// Subject extracts what a rule pattern is matched against for a call.
func Subject(tool string, input json.RawMessage) string {
	var in map[string]any
	if json.Unmarshal(input, &in) != nil {
		return ""
	}
	str := func(k string) string {
		v, _ := in[k].(string)
		return v
	}
	switch tool {
	case "bash":
		return strings.TrimSpace(str("command"))
	case "read_file", "write_file", "edit_file", "list_dir":
		return filepath.Clean(str("path"))
	case "glob", "grep":
		if p := str("path"); p != "" {
			return filepath.Clean(p)
		}
		return "."
	case "fetch_url":
		// The rewrite that turns a GitHub blob page into the raw file
		// happens before the check, so the host approved, and any rule
		// saved, is the host actually contacted.
		out, _ := tools.NormalizeURL(str("url"))
		return out
	}
	for _, k := range []string{"path", "command", "url", "query", "name"} {
		if v := str(k); v != "" {
			return v
		}
	}
	return ""
}

// SubjectIn is Subject for a session working in root: a bash command's
// leading `cd <root> &&` is dropped (see tools.TrimWorkspaceCd), since it
// changes nothing about what runs and would otherwise make every command
// a model prefixes that way look like a `cd` to rules and to Suggest.
func SubjectIn(tool string, input json.RawMessage, root string) string {
	s := Subject(tool, input)
	if tool == "bash" {
		s = tools.TrimWorkspaceCd(s, root)
	}
	return s
}

// Suggest proposes the rule to save when the user says "always allow
// this": the command's leading words for bash, the host for URLs, the
// directory for file tools, the bare tool otherwise. For a bash command it
// cannot capture safely — a compound command (&&, ;, |, $( ), backticks),
// or one led by a builtin or a wrapper that runs something else (cd, sudo,
// sh -c, env, xargs, ...) — it returns the zero Rule: a pattern ending in
// * would allow whatever follows the leading words (see Match).
func Suggest(tool, subject string) Rule {
	switch tool {
	case "bash":
		words := strings.Fields(subject)
		if len(words) == 0 {
			return Rule{Tool: tool}
		}
		// 2>&1 joins two outputs; it is not a second command.
		if bare := strings.ReplaceAll(subject, "2>&1", ""); strings.ContainsAny(bare, ";|&`\n") || strings.Contains(bare, "$(") || notCapturable[words[0]] {
			return Rule{}
		}
		if len(words) == 1 {
			return Rule{Tool: tool, Pattern: words[0]}
		}
		// Runners take a subcommand that decides what the command does, so
		// the rule keeps it: "go test *" rather than "go *".
		n := 1
		if runners[words[0]] && !strings.HasPrefix(words[1], "-") && isWordy(words[1]) {
			n = 2
		}
		return Rule{Tool: tool, Pattern: strings.Join(words[:n], " ") + " *"}
	case "fetch_url":
		if u, err := url.Parse(subject); err == nil && u.Host != "" {
			return Rule{Tool: tool, Pattern: u.Scheme + "://" + u.Host + "/*"}
		}
		return Rule{Tool: tool}
	case "read_file", "write_file", "edit_file", "list_dir", "glob", "grep":
		dir := filepath.Dir(subject)
		if dir == "." || dir == "/" || subject == "" {
			return Rule{Tool: tool}
		}
		return Rule{Tool: tool, Pattern: dir + "/*"}
	}
	return Rule{Tool: tool}
}

// notCapturable are leading words a rule must not be made of: navigation
// and environment builtins, and wrappers that run another command, so
// their "leading words" say nothing about what the command does.
var notCapturable = map[string]bool{
	"cd": true, "pushd": true, "popd": true, "export": true, "env": true, "sudo": true, "doas": true,
	"sh": true, "bash": true, "zsh": true, "dash": true, "eval": true, "exec": true, "xargs": true,
	"time": true, "nohup": true, "nice": true, "timeout": true, "command": true, "builtin": true, "source": true, ".": true,
}

// runners are commands whose first argument is a subcommand.
var runners = map[string]bool{
	"go": true, "git": true, "npm": true, "npx": true, "yarn": true, "pnpm": true, "bun": true, "cargo": true,
	"docker": true, "kubectl": true, "pip": true, "pip3": true, "uv": true, "poetry": true, "gh": true, "systemctl": true,
	"python": true, "python3": true, "node": true, "dotnet": true, "gradle": true, "mvn": true, "terraform": true,
}

func isWordy(w string) bool {
	for _, r := range w {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.' || r == ':') {
			return false
		}
	}
	return true
}

// File is the JSON shape of a rule file: {"allow": [...], "deny": [...]}.
type File struct {
	Allow []string `json:"allow,omitempty"`
	Deny  []string `json:"deny,omitempty"`
}

// ProjectFile is the per-project rule file, relative to the project root.
const ProjectFile = ".janetcode/permissions.json"

// LoadProject reads the project's rule file. A missing file gives empty
// rules and an empty hash; the hash identifies the approved content.
func LoadProject(root string) (Rules, string, error) {
	data, err := os.ReadFile(filepath.Join(root, ProjectFile))
	if errors.Is(err, os.ErrNotExist) {
		return Rules{}, "", nil
	}
	if err != nil {
		return Rules{}, "", err
	}
	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		return Rules{}, "", fmt.Errorf("%s: %w", ProjectFile, err)
	}
	rs, err := f.Rules()
	if err != nil {
		return Rules{}, "", fmt.Errorf("%s: %w", ProjectFile, err)
	}
	sum := sha256.Sum256(data)
	return rs, hex.EncodeToString(sum[:]), nil
}

// Rules parses the file's entries.
func (f File) Rules() (Rules, error) {
	allow, err := ParseAll(f.Allow)
	if err != nil {
		return Rules{}, err
	}
	deny, err := ParseAll(f.Deny)
	if err != nil {
		return Rules{}, err
	}
	return Rules{Allow: allow, Deny: deny}, nil
}
