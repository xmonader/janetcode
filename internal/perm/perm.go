// Package perm holds persistent permission rules: which tool calls run
// without asking and which are refused outright. A rule is "tool",
// "tool(pattern)" or "tool(param:pattern, ...)". A bare pattern is
// matched against the call's subject: the command for bash, the path for
// file tools, the URL for fetch_url. A parameter clause names a field of
// the call instead (see param.go), and every clause of a rule must match.
// Deny rules win over allow rules, a hard deny (see HardDeny) wins over
// both, and anything unmatched asks.
package perm

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/xmonader/janetcode/internal/shell"
	"github.com/xmonader/janetcode/internal/weburl"
)

// Rule is one allow or deny entry.
//
// A rule is matched against every call of its tool, so its pattern is
// parsed once — when the rule is made, by Parse or by compile — and not
// again per call. The parse is kept in unexported fields, which is why
// rules are compared with Equal rather than ==: two rules mean the same
// thing when their tool and pattern do.
type Rule struct {
	Tool    string
	Pattern string // "" matches every call of the tool

	// params is the pattern read as parameter clauses, and paramRule
	// whether it is that form at all (see param.go). compiled says the
	// two have been worked out; a Rule built as a struct literal works
	// them out the first time it is matched.
	params    []Param
	paramRule bool
	compiled  bool
}

// Equal reports whether two rules say the same thing.
func (r Rule) Equal(o Rule) bool { return r.Tool == o.Tool && r.Pattern == o.Pattern }

// compile parses the pattern once. A malformed parameter pattern is
// reported by Parse, which is where a typo can still be pointed at
// whoever wrote it; here it simply means "not a parameter rule", and the
// pattern is matched against the subject as it always was.
func (r Rule) compile() Rule {
	if r.compiled {
		return r
	}
	if r.Pattern != "" {
		if ps, ok, err := Params(r.Pattern); err == nil && ok {
			r.params, r.paramRule = ps, true
		}
	}
	r.compiled = true
	return r
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
	r := Rule{Tool: strings.TrimSpace(s[:i]), Pattern: strings.TrimSpace(s[i+1 : len(s)-1])}
	// A parameter rule is validated here rather than at match time, so a
	// typo is reported to whoever wrote it instead of quietly matching
	// nothing. Its spacing is canonicalised so the same rule written two
	// ways is one rule to Add, to the ledger and to the eye.
	ps, isParam, err := Params(r.Pattern)
	if err != nil {
		return Rule{}, err
	}
	if isParam {
		r.Pattern = FormatParams(ps)
		r.params, r.paramRule = ps, true
	}
	r.compiled = true
	return r, nil
}

// FormatParams writes parameter clauses the canonical way.
func FormatParams(ps []Param) string {
	parts := make([]string, 0, len(ps))
	for _, p := range ps {
		parts = append(parts, p.Name+":"+p.Pattern)
	}
	return strings.Join(parts, ", ")
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

// Rules is a set of allow, deny and ask rules. An ask rule marks a call
// as sensitive: it is put to the user even when an allow rule, a session
// allowance, /auto, /edits or --yolo would have let it run, and even when
// the tool only reads (see DecideCall).
type Rules struct {
	Allow []Rule
	Deny  []Rule
	Ask   []Rule
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

// Call is one tool call as the rules see it: the tool's name, the subject
// a bare pattern is matched against (see Subject), and the raw input a
// parameter clause reads its fields from. Root is the workspace, used by
// the checks that judge what a command would write.
//
// A Call with no Input still matches every rule that existed before
// parameters did; a parameter rule simply never matches one, because
// nothing was given for it to look at.
type Call struct {
	Tool    string
	Subject string
	Input   json.RawMessage
	Root    string
}

// Decide matches a call by tool and subject alone. Deny wins; the
// matching rule is returned. Parameter rules cannot match here — use
// DecideCall, which carries the call's input.
func (rs Rules) Decide(tool, subject string) (Verdict, Rule) {
	v, r, _ := rs.DecideCall(Call{Tool: tool, Subject: subject})
	return v, r
}

// DecideCall matches a whole call. The order is the order of authority:
// a hard deny (see HardDeny) refuses whatever the rules say and comes
// back with the reason; then deny rules, so a deny always outranks an
// allow however the two were written; then ask rules, which come back as
// Ask with the rule that matched (a non-zero Rule: the call is sensitive
// and must be asked about whatever else would allow it); then allow
// rules; anything unmatched asks with the zero Rule.
func (rs Rules) DecideCall(c Call) (Verdict, Rule, string) {
	if why := HardDeny(c); why != "" {
		return Denied, Rule{Tool: c.Tool}, why
	}
	dc := denyView(c)
	for _, r := range rs.Deny {
		if r.matchesAny(dc) || r.matchesSegment(dc) {
			return Denied, r, ""
		}
	}
	for _, r := range rs.Ask {
		if r.matchesAny(dc) || r.matchesSegment(dc) {
			return Ask, r, ""
		}
	}
	if r, ok := rs.allows(c); ok {
		return Allowed, r, ""
	}
	return Ask, Rule{}, ""
}

// AllowedBy reports the allow rule that would cover the call if no deny
// or ask rule stood in the way: what a refusal or a forced prompt names
// as the rule it outranked, so a conflict says which rule won and why.
func (rs Rules) AllowedBy(c Call) (Rule, bool) { return rs.allows(c) }

// denyView is the call as deny and ask rules see it: a bash line with
// its here-documents taken out, since a body is the command's stdin and
// not commands — split at its newlines, a commit message mentioning
// "rm -rf" was refused by bash(rm *). A here-document fed to something
// that runs it (bash <<EOF, ssh host <<EOF, python3 - <<EOF) is code, and
// is kept (see shell.HeredocRunsCode).
func denyView(c Call) Call {
	if c.Tool != "bash" || !strings.Contains(c.Subject, "<<") || shell.HeredocRunsCode(c.Subject) {
		return c
	}
	c.Subject = shell.StripHeredocs(c.Subject)
	return c
}

// dupRedirect is a descriptor duplication such as 2>&1 or >&2. Its & is
// not a command separator, so it is taken out before a line is split.
var dupRedirect = regexp.MustCompile(`[0-9]*>&[0-9-]+`)

// allows is the allow half of DecideCall. A single command is matched
// the way it always was: the first allow rule that matches it wins.
//
// A compound bash line (split at pipes, &&, ||, ;, & and newlines, see
// shell.Segments) is allowed only when every command in it is: each one
// is matched by some allow rule, or is a known-safe read (shell.SafeRead,
// the classifier AUTO mode trusts), and at least one of them was matched
// by a rule. A rule is a prefix and * spans shell operators, so matching
// the whole line let bash(go test *) allow `go test ./... && curl ...`:
// one trusted command at the front allowed whatever the model put after
// it. The whole line still counts when a rule names it exactly, with no
// wildcard, since then the user wrote out everything that runs.
//
// A command or process substitution — $(…), `…`, <(…), >(…) — is held to
// the same test: it is a command line of its own that runs first, so
// bash(go test *) matched against `go test $(curl …)` allowed the curl.
// The line is judged with each substitution standing as one word, and
// what each one runs is judged as a line of its own, recursively.
//
// A here-document's body is taken out first (shell.StripHeredocs): it is
// the command's stdin, and split at its newlines it read as commands no
// rule names, so bash(git commit *) stopped allowing a commit whose
// message came from `<<'EOF'`.
func (rs Rules) allows(c Call) (Rule, bool) {
	var segs []string
	var bodies []string
	if c.Tool == "bash" {
		var outer string
		outer, bodies = shell.SplitSubstitutions(shell.StripHeredocs(c.Subject))
		segs = shell.Segments(dupRedirect.ReplaceAllString(outer, " "))
	}
	if len(segs) < 2 && len(bodies) == 0 {
		for _, r := range rs.Allow {
			if r.matchesCall(c) {
				return r, true
			}
		}
		return Rule{}, false
	}
	for _, r := range rs.Allow {
		if r.Pattern != "" && !strings.ContainsAny(r.Pattern, "*?") && !r.compile().paramRule && r.matchesCall(c) {
			return r, true
		}
	}
	return rs.lineAllowed(c, segs, bodies, 0)
}

// maxSubstDepth bounds how deeply nested substitutions are followed; a
// line nested deeper than this is not allowed by the rules at all.
const maxSubstDepth = 8

// lineAllowed is allows for one command line already split into its
// commands (segs) and the lines it substitutes (bodies): every command
// must be allowed by a rule or be a safe read, every body must pass the
// same test, and the first rule that matched anything is returned.
func (rs Rules) lineAllowed(c Call, segs, bodies []string, depth int) (Rule, bool) {
	if depth > maxSubstDepth {
		return Rule{}, false
	}
	var first Rule
	matched := false
	for _, s := range segs {
		if s = strings.TrimSpace(s); s == "" {
			continue
		}
		sc := Call{Tool: c.Tool, Subject: s, Input: withCommand(c.Input, s), Root: c.Root}
		found := false
		for _, r := range rs.Allow {
			if r.matchesCall(sc) {
				if !matched {
					first, matched = r, true
				}
				found = true
				break
			}
		}
		if !found && !shell.SafeRead(s) {
			return Rule{}, false
		}
	}
	for _, b := range bodies {
		outer, inner := shell.SplitSubstitutions(b)
		r, ok := rs.lineAllowed(c, shell.Segments(dupRedirect.ReplaceAllString(outer, " ")), inner, depth+1)
		if !ok {
			// Either something in it is not allowed, or nothing in it was
			// matched by a rule — which is fine when all of it is a safe
			// read.
			if !shell.SafeRead(b) {
				return Rule{}, false
			}
			continue
		}
		if !matched {
			first, matched = r, true
		}
	}
	return first, matched
}

// withCommand is a bash call's input with its command replaced by one
// segment of it, so a parameter rule (bash(command:make *)) is matched
// against that segment rather than the whole line. An input that does
// not decode comes back nil, which no parameter rule matches.
func withCommand(input json.RawMessage, cmd string) json.RawMessage {
	var in map[string]any
	if len(input) == 0 || json.Unmarshal(input, &in) != nil {
		return nil
	}
	in["command"] = cmd
	out, err := json.Marshal(in)
	if err != nil {
		return nil
	}
	return out
}

// Add appends a rule unless an equal one exists.
func (rs *Rules) Add(v Verdict, r Rule) bool {
	list := &rs.Allow
	if v == Denied {
		list = &rs.Deny
	}
	for _, e := range *list {
		if e.Equal(r) {
			return false
		}
	}
	// Parsed here rather than on every call it is then matched against.
	*list = append(*list, r.compile())
	return true
}

// addAsk appends an ask rule unless an equal one exists.
func (rs *Rules) addAsk(r Rule) bool {
	for _, e := range rs.Ask {
		if e.Equal(r) {
			return false
		}
	}
	rs.Ask = append(rs.Ask, r.compile())
	return true
}

// AddAsk adds an ask (sensitive) rule.
func (rs *Rules) AddAsk(r Rule) bool { return rs.addAsk(r) }

// hasAllow reports whether an equal rule is already saved as an allow.
func (rs Rules) hasAllow(r Rule) bool {
	for _, e := range rs.Allow {
		if e.Equal(r) {
			return true
		}
	}
	return false
}

// Merge returns the union of both sets, a's rules first.
func Merge(a, b Rules) Rules {
	out := Rules{}
	for _, r := range append(append([]Rule{}, a.Ask...), b.Ask...) {
		out.addAsk(r)
	}
	for _, r := range append(append([]Rule{}, a.Allow...), b.Allow...) {
		out.Add(Allowed, r)
	}
	for _, r := range append(append([]Rule{}, a.Deny...), b.Deny...) {
		out.Add(Denied, r)
	}
	return out
}

func (r Rule) matches(tool, subject string) bool {
	return r.matchesCall(Call{Tool: tool, Subject: subject})
}

// matchesCall is the whole matcher, as an allow rule reads it: the tool
// has to be the call's (or "*"), and then either the bare pattern
// matches the subject or every parameter clause matches its field. A
// typed scope (see scope.go) with several values has to hold for all of
// them.
func (r Rule) matchesCall(c Call) bool { return r.match(c, modeAll) }

// matchesAny is matchesCall as a deny or ask rule reads it: a typed
// scope matches when any of its values does.
func (r Rule) matchesAny(c Call) bool { return r.match(c, modeAny) }

func (r Rule) match(c Call, mode matchMode) bool {
	if r.Tool != c.Tool && r.Tool != "*" {
		return false
	}
	if r.Pattern == "" {
		return true
	}
	r = r.compile()
	if !r.paramRule {
		if Match(r.Pattern, c.Subject) {
			return true
		}
		alt := otherSpelling(c.Tool, c.Subject, c.Root)
		return alt != "" && Match(r.Pattern, alt)
	}
	if len(c.Input) == 0 {
		return false
	}
	f := fields{tool: c.Tool, input: c.Input, root: c.Root}
	for _, p := range r.params {
		if isScope(p.Name, c.Tool) {
			if !scopeMatch(p, c, &f, mode) {
				return false
			}
			continue
		}
		if !matchAny(p.Pattern, f.values(p.Name)) {
			return false
		}
	}
	return true
}

// matchesSegment reports whether a bare bash pattern matches any one
// command of a compound line (split at pipes, &&, ||, ; and newlines, see
// shell.Segments). It is what a deny rule is also matched with: a rule is
// a prefix, so bash(rm *) matched against the whole line was walked past
// by `ls && rm -rf src`, and a deny that the model can step around by
// putting any command first denies nothing. Allow rules are not matched
// this way — one allowed segment must not allow the rest of the line.
func (r Rule) matchesSegment(c Call) bool {
	if c.Tool != "bash" || (r.Tool != "bash" && r.Tool != "*") || r.Pattern == "" {
		return false
	}
	if r = r.compile(); r.paramRule {
		return false
	}
	segs := shell.Segments(c.Subject)
	if len(segs) >= 2 {
		for _, s := range segs {
			if s = strings.TrimSpace(s); s != "" && Match(r.Pattern, s) {
				return true
			}
		}
	}
	// A substitution runs a command line of its own — `echo $(git push)`
	// pushes — so a deny rule is matched against every command inside
	// one as well, however deeply nested.
	_, bodies := shell.SplitSubstitutions(c.Subject)
	for depth := 0; len(bodies) > 0 && depth <= maxSubstDepth; depth++ {
		var next []string
		for _, b := range bodies {
			outer, inner := shell.SplitSubstitutions(b)
			for _, s := range shell.Segments(outer) {
				if s = strings.TrimSpace(s); s != "" && Match(r.Pattern, s) {
					return true
				}
			}
			next = append(next, inner...)
		}
		bodies = next
	}
	return false
}

// otherSpelling is the second way a file tool's path names the same
// file: the path relative to the workspace for an absolute one inside it,
// and the absolute path for a relative one. The file tools take either,
// so a rule has to hold for both: a deny rule written secrets/* was walked
// past by /work/proj/secrets/key, and an allow rule saved from an
// absolute path never matched the relative spelling of the same file. ""
// when there is none — not a file tool, no workspace, or an absolute path
// outside it.
func otherSpelling(tool, p, root string) string {
	if !fileTools[tool] || root == "" || p == "" {
		return ""
	}
	root = filepath.Clean(root)
	if !filepath.IsAbs(p) {
		return filepath.Join(root, p)
	}
	rel, err := filepath.Rel(root, filepath.Clean(p))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	return rel
}

// matchAny reports whether the pattern matches any of a field's values;
// a field that is absent has none, so a clause naming it never matches.
func matchAny(pattern string, vals []string) bool {
	for _, v := range vals {
		if Match(pattern, v) {
			return true
		}
	}
	return false
}

// Match is a wildcard match where * spans anything (including / and
// spaces) and ? one character. A pattern without wildcards must equal the
// subject; commands are compared after whitespace normalisation.
//
// * spans shell operators too: `bash(go test *)` matches
// `go test ./... && rm -rf y`. A rule is a prefix the user trusts, not a
// parse of the command, which is why Suggest never proposes one for a
// command whose leading words do not decide everything that runs, and
// why DecideCall matches a compound bash line one command at a time
// (see allows and matchesSegment) rather than through Match alone.
func Match(pattern, subject string) bool {
	pattern = strings.Join(strings.Fields(pattern), " ")
	subject = strings.Join(strings.Fields(subject), " ")
	return glob(pattern, subject)
}

// glob matches with a single backtrack point rather than recursion. The
// recursive form was exponential on a pattern with several stars — a
// rule like bash(go *test *test *x) against a long command the model
// chose could take longer than the session — and this is the same
// language: * spans any bytes, ? exactly one.
func glob(p, s string) bool {
	star, mark := -1, 0
	i, j := 0, 0
	for j < len(s) {
		switch {
		case i < len(p) && (p[i] == '?' || p[i] == s[j]):
			i, j = i+1, j+1
		case i < len(p) && p[i] == '*':
			// Remember where the star was and try matching nothing first.
			star, mark = i, j
			i++
		case star >= 0:
			// Back up: let the last star swallow one more byte.
			mark++
			i, j = star+1, mark
		default:
			return false
		}
	}
	for i < len(p) && p[i] == '*' {
		i++
	}
	return i == len(p)
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
		out, _ := weburl.Normalize(str("url"))
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
// leading `cd <root> &&` is dropped (see shell.TrimWorkspaceCd), since it
// changes nothing about what runs and would otherwise make every command
// a model prefixes that way look like a `cd` to rules and to Suggest.
func SubjectIn(tool string, input json.RawMessage, root string) string {
	s := Subject(tool, input)
	if tool == "bash" {
		s = shell.TrimWorkspaceCd(s, root)
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
//
// Suggest is one rung of SuggestLadder: the one a front-end starts on.
func Suggest(tool, subject string) Rule {
	rungs, def := ladder(tool, subject)
	if len(rungs) == 0 {
		return Rule{}
	}
	return rungs[def]
}

// SuggestLadder is the range of rules the user may choose between when
// saving one for a call, ordered narrowest first: for
// `go test ./internal/...` it is bash(go test ./internal/...),
// bash(go test *), bash(go *); for a file tool the exact path, its
// directory, then the directory above. The widest rung is still narrower
// than the tool itself, so no rung ever amounts to bash(*).
//
// A call Suggest refuses to capture (see above) has no ladder either:
// widening is only offered where a rule was safe to propose at all.
func SuggestLadder(tool, subject string) []Rule {
	rungs, _ := ladder(tool, subject)
	return rungs
}

// DefaultRung is the index in rungs of the rule Suggest proposes, or 0
// when it is not among them. Front-ends start the user there, so that
// answering without moving saves exactly what it used to.
func DefaultRung(rungs []Rule, r Rule) int {
	for i, e := range rungs {
		if e.Equal(r) {
			return i
		}
	}
	return 0
}

// ladder builds the rungs and says which one is the default.
func ladder(tool, subject string) ([]Rule, int) {
	switch tool {
	case "bash":
		return bashLadder(tool, subject)
	case "fetch_url":
		return urlLadder(tool, subject)
	case "read_file", "write_file", "edit_file", "list_dir", "glob", "grep":
		return pathLadder(tool, subject)
	}
	return []Rule{{Tool: tool}}, 0
}

func bashLadder(tool, subject string) ([]Rule, int) {
	words := strings.Fields(subject)
	if len(words) == 0 {
		return []Rule{{Tool: tool}}, 0
	}
	// 2>&1 joins two outputs; it is not a second command.
	// The leading word is judged by its name, so /usr/bin/sudo is sudo,
	// and an assignment prefix (FOO=1 cmd) is env without the word: its
	// rule, FOO=1 *, would allow any command written after it.
	if bare := strings.ReplaceAll(subject, "2>&1", ""); strings.ContainsAny(bare, ";|&`\n") || strings.Contains(bare, "$(") ||
		notCapturable[words[0]] || notCapturable[filepath.Base(words[0])] || isAssignment(words[0]) {
		return nil, 0
	}
	// The exact command is the narrowest rung: no wildcard at all.
	rungs := []Rule{{Tool: tool, Pattern: strings.Join(words, " ")}}
	// A command that reaches the network is widened by the host, never
	// past it: approving curl https://api.github.com/x offers
	// bash(cmd:curl *, host:api.github.com), not curl *, which would
	// allow the same command against any host. Two hosts in one command
	// have no rung that covers both without covering more.
	if hosts := shell.NetworkHosts(subject); len(hosts) > 0 {
		if len(hosts) == 1 && !strings.ContainsAny(hosts[0], "*?, ()") {
			rungs = append(rungs, Rule{Tool: tool, Pattern: "cmd:" + words[0] + " *, host:" + hosts[0]}.compile())
			return rungs, 1
		}
		return rungs, 0
	}
	// Runners take a subcommand that decides what the command does, so
	// there is a rung that keeps it — "go test *" — above which sits the
	// runner alone, "go *". The floor is the leading word: never bash
	// itself, which would be bash(*).
	if len(words) > 1 && runners[words[0]] && !strings.HasPrefix(words[1], "-") && isWordy(words[1]) {
		rungs = append(rungs, Rule{Tool: tool, Pattern: words[0] + " " + words[1] + " *"})
	}
	// A one-word command has nowhere to widen to: "make *" does not even
	// match `make`, and bash alone is bash(*).
	if len(words) == 1 {
		return rungs, 0
	}
	rungs = append(rungs, Rule{Tool: tool, Pattern: words[0] + " *"})
	rungs = dedupe(rungs)
	// The default is what Suggest has always proposed: the leading words
	// plus *, which is the widest rung unless the subcommand made one.
	def := len(rungs) - 1
	if len(rungs) > 2 {
		def = len(rungs) - 2
	}
	return rungs, def
}

func urlLadder(tool, subject string) ([]Rule, int) {
	u, err := url.Parse(subject)
	if err != nil || u.Host == "" {
		return []Rule{{Tool: tool}}, 0
	}
	host := u.Scheme + "://" + u.Host
	rungs := []Rule{{Tool: tool, Pattern: subject}}
	// One rung for the section of the site the page is in, so trusting
	// one host's docs does not have to mean trusting the whole host.
	if seg := strings.SplitN(strings.TrimPrefix(u.Path, "/"), "/", 2)[0]; seg != "" {
		rungs = append(rungs, Rule{Tool: tool, Pattern: host + "/" + seg + "/*"})
	}
	rungs = append(rungs, Rule{Tool: tool, Pattern: host + "/*"})
	rungs = dedupe(rungs)
	return rungs, len(rungs) - 1
}

func pathLadder(tool, subject string) ([]Rule, int) {
	if subject == "" || subject == "." {
		return []Rule{{Tool: tool}}, 0
	}
	rungs := []Rule{{Tool: tool, Pattern: subject}}
	dir := filepath.Dir(subject)
	if dir == "." || dir == "/" {
		// Nothing to widen to but the tool itself, which is what
		// Suggest has always proposed here.
		return append(rungs, Rule{Tool: tool}), 1
	}
	rungs = append(rungs, Rule{Tool: tool, Pattern: dir + "/*"})
	// One level above the directory is the tree the file sits in. Higher
	// than that is guesswork, and the root is never a rung.
	if up := filepath.Dir(dir); up != "." && up != "/" && up != dir {
		rungs = append(rungs, Rule{Tool: tool, Pattern: up + "/*"})
	}
	return dedupe(rungs), 1
}

// dedupe drops rungs equal to one already in the list, keeping order: a
// one-word command and a path with a single parent both collapse.
func dedupe(rungs []Rule) []Rule {
	out := rungs[:0:0]
	for _, r := range rungs {
		seen := false
		for _, e := range out {
			if e.Equal(r) {
				seen = true
				break
			}
		}
		if !seen {
			out = append(out, r)
		}
	}
	return out
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

// isAssignment reports whether a shell word is a NAME=value assignment.
func isAssignment(w string) bool {
	name, _, ok := strings.Cut(w, "=")
	if !ok || name == "" {
		return false
	}
	for i, r := range name {
		if !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || i > 0 && r >= '0' && r <= '9') {
			return false
		}
	}
	return true
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
	// Ask are sensitive-call rules: a call they match is always asked
	// about (see Rules.Ask).
	Ask []string `json:"ask,omitempty"`
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
	ask, err := ParseAll(f.Ask)
	if err != nil {
		return Rules{}, err
	}
	return Rules{Allow: allow, Deny: deny, Ask: ask}, nil
}

// SplitList splits a comma-separated list of rules, as --allow and
// --deny take them, at the commas between rules and not at the ones
// inside a rule's parentheses: bash(cmd:curl *, host:x.example),read_file
// is two rules.
func SplitList(s string) []string {
	var out []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				if part := strings.TrimSpace(s[start:i]); part != "" {
					out = append(out, part)
				}
				start = i + 1
			}
		}
	}
	if part := strings.TrimSpace(s[start:]); part != "" {
		out = append(out, part)
	}
	return out
}

// Explain says in one line what the rules decide about a call and why:
// which rule won, and which allow rule it outranked when one would have
// covered the call too. It is /permissions check's answer.
func (rs Rules) Explain(c Call) string {
	v, r, hard := rs.DecideCall(c)
	allow, allowed := rs.AllowedBy(c)
	outranks := ""
	if allowed {
		outranks = ", which outranks allow rule " + allow.String()
	}
	switch {
	case hard != "":
		return "refused whatever the rules say: this command " + hard
	case v == Denied:
		return "denied by rule " + r.String() + outranks
	case v == Ask && !r.IsZero():
		return "always asks because of rule " + r.String() + outranks + " (in every mode, --yolo included)"
	case v == Allowed:
		return "allowed by rule " + r.String() + " (runs without asking in every mode)"
	}
	return "no rule matches: whether it asks depends on the mode"
}
