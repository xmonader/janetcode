package perm

import (
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/xmonader/janetcode/internal/weburl"
)

// A rule's pattern is matched against one subject string per tool (see
// Subject): the command for bash, the path for a file tool, the URL for
// fetch_url. That is the right subject most of the time and it is what
// every rule written so far means. It cannot say anything about the other
// fields of a call, though — which model a task runs on, which agent, the
// host of a URL rather than the whole of it — so those calls could only
// ever be allowed as a whole tool.
//
// The parameter form names a field instead:
//
//	task(model:haiku*)
//	task(agent:explore, model:haiku*)
//	fetch_url(domain:*.example.com)
//	edit_file(path:internal/**)
//
// Several parameters are separated by commas and all of them must match.
// A rule with no colon-led parameter keeps its old meaning exactly —
// bash(npm run *) is still matched against the command — so no rule
// written before this existed changes behaviour.

// Param is one "name:pattern" clause of a parameter rule.
type Param struct {
	Name    string
	Pattern string
}

// paramName is what may stand left of the colon: a JSON field name, or a
// dotted path into nested objects ("input.path").
var paramName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*(?:\.[A-Za-z_0-9-]+)*$`)

// Params parses a rule pattern as the parameter form and reports whether
// it is one at all. A pattern that is not (the ordinary subject pattern)
// comes back with ok false and no error; a pattern that clearly meant to
// be one but is malformed comes back with an error.
func Params(pattern string) (ps []Param, ok bool, err error) {
	segs := splitParams(pattern)
	if len(segs) == 0 {
		return nil, false, nil
	}
	// The form is decided by the first clause alone: everything after it
	// then has to be a clause too, so a typo is an error rather than a
	// pattern that silently means something else.
	if _, _, isParam := paramClause(segs[0]); !isParam {
		return nil, false, nil
	}
	seen := map[string]bool{}
	for _, seg := range segs {
		name, pat, isParam := paramClause(seg)
		switch {
		case strings.TrimSpace(seg) == "":
			return nil, true, fmt.Errorf("bad rule: empty parameter in %q", pattern)
		case !isParam:
			return nil, true, fmt.Errorf("bad rule %q: want param:pattern for every parameter, got %q", pattern, strings.TrimSpace(seg))
		case pat == "":
			return nil, true, fmt.Errorf("bad rule %q: parameter %q has an empty pattern", pattern, name)
		case seen[name]:
			return nil, true, fmt.Errorf("bad rule %q: parameter %q named twice", pattern, name)
		}
		seen[name] = true
		ps = append(ps, Param{Name: name, Pattern: pat})
	}
	return ps, true, nil
}

// splitParams breaks a pattern at commas.
func splitParams(pattern string) []string {
	if pattern == "" {
		return nil
	}
	return strings.Split(pattern, ",")
}

// paramClause reads one "name:pattern" clause. Two things that look like
// one are not: a URL, whose scheme colon is followed by "//", and a
// Windows path, whose drive letter is a single letter followed by a
// separator (read_file(C:/x) names a file, not a field "C").
func paramClause(seg string) (name, pattern string, ok bool) {
	i := strings.IndexByte(seg, ':')
	if i < 0 {
		return "", "", false
	}
	name = strings.TrimSpace(seg[:i])
	pattern = strings.TrimSpace(seg[i+1:])
	if !paramName.MatchString(name) || strings.HasPrefix(pattern, "//") {
		return "", "", false
	}
	if len(name) == 1 && (strings.HasPrefix(pattern, "/") || strings.HasPrefix(pattern, `\`)) {
		return "", "", false // a drive letter, not a field
	}
	return name, pattern, true
}

// fileTools take a path argument that is cleaned before it is matched, so
// a rule can be written the way the path is written in the repository.
var fileTools = map[string]bool{
	"read_file": true, "write_file": true, "edit_file": true, "list_dir": true,
	"glob": true, "grep": true,
}

// FieldValues is what a parameter matches against: the call's JSON field
// named by a dotted path, flattened to strings. A list field yields one
// value per element, so a rule matches when any element does.
//
// fetch_url(domain:…) is special-cased on top of the URL: the host is
// what a person means by trusting a site, and it is not a field of the
// call at all.
func FieldValues(tool string, input json.RawMessage, name string) []string {
	f := fields{tool: tool, input: input}
	return f.values(name)
}

// fields is a call's input decoded at most once, however many clauses a
// rule then reads from it. A rule with three parameters used to unmarshal
// the whole call three times, on every call it was matched against.
type fields struct {
	tool    string
	input   json.RawMessage
	root    string // the workspace, for a path's other spelling
	decoded any
	done    bool
}

func (f *fields) values(name string) []string {
	if f.tool == "fetch_url" && (name == "domain" || name == "host") {
		return urlHosts(f.input)
	}
	if !f.done {
		f.done = true
		if json.Unmarshal(f.input, &f.decoded) != nil {
			f.decoded = nil
		}
	}
	if f.decoded == nil {
		return nil
	}
	return f.lookupValues(name)
}

func (f *fields) lookupValues(name string) []string {
	tool, in := f.tool, f.decoded
	v, ok := lookup(in, strings.Split(name, "."))
	if !ok {
		return nil
	}
	vals := flatten(v)
	if name == "path" && fileTools[tool] {
		n := len(vals)
		for i := 0; i < n; i++ {
			if s := vals[i]; s != "" {
				vals[i] = filepath.ToSlash(filepath.Clean(s))
				// Either spelling of the path is the path (see
				// otherSpelling), so a clause may match either.
				if alt := otherSpelling(tool, filepath.Clean(s), f.root); alt != "" {
					vals = append(vals, filepath.ToSlash(alt))
				}
			}
		}
	}
	return vals
}

// urlHosts is the host of a fetch_url call, with and without its port, so
// both domain:example.com and domain:example.com:8080 can be written.
func urlHosts(input json.RawMessage) []string {
	var in struct {
		URL string `json:"url"`
	}
	if json.Unmarshal(input, &in) != nil || in.URL == "" {
		return nil
	}
	raw, _ := weburl.Normalize(in.URL)
	if raw == "" {
		raw = in.URL
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return nil
	}
	if h := u.Hostname(); h != u.Host {
		return []string{u.Host, h}
	}
	return []string{u.Host}
}

// lookup walks a dotted path through decoded JSON.
func lookup(v any, path []string) (any, bool) {
	for _, key := range path {
		m, ok := v.(map[string]any)
		if !ok {
			return nil, false
		}
		v, ok = m[key]
		if !ok {
			return nil, false
		}
	}
	return v, true
}

// flatten turns a JSON value into the strings a pattern may match.
func flatten(v any) []string {
	switch t := v.(type) {
	case nil:
		return nil
	case string:
		return []string{t}
	case bool:
		return []string{strconv.FormatBool(t)}
	case float64:
		return []string{strconv.FormatFloat(t, 'f', -1, 64)}
	case []any:
		var out []string
		for _, e := range t {
			out = append(out, flatten(e)...)
		}
		return out
	default:
		data, err := json.Marshal(t)
		if err != nil {
			return nil
		}
		return []string{string(data)}
	}
}
