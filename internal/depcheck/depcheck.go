// Package depcheck looks at the dependencies an edit adds to a manifest
// — go.mod, package.json, requirements*.txt — and asks the public
// registry two questions about each new name: does it exist, and how
// long has it existed.
//
// Models invent package names, and they invent the same ones again: in
// one study 43% of hallucinated names reappeared on every rerun of the
// same prompt, which makes them names worth registering for anyone who
// wants their code run. A name that does not exist is a hallucination
// about to fail the build; a name first published a few days ago may be
// someone who registered the hallucination. Either is worth a line to
// the model and the user before the change is built on.
//
// It only flags; it never refuses an edit. Every lookup fails open: no
// network, a slow registry, a private module or an egress policy that
// refuses the registry each leave the dependency unflagged, and
// JANETCODE_OFFLINE skips the lookups entirely.
package depcheck

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Ecosystems.
const (
	Go   = "go"
	NPM  = "npm"
	PyPI = "pypi"
)

// YoungAge is how recently a package may have been first published before
// it is flagged.
const YoungAge = 30 * 24 * time.Hour

// Ecosystem is the ecosystem whose manifest path is, or "".
func Ecosystem(path string) string {
	base := filepath.Base(path)
	switch {
	case base == "go.mod":
		return Go
	case base == "package.json":
		return NPM
	case strings.HasPrefix(base, "requirements") && strings.HasSuffix(base, ".txt"):
		return PyPI
	}
	return ""
}

// Parse lists the dependency names a manifest declares.
func Parse(eco string, data []byte) []string {
	switch eco {
	case Go:
		return parseGoMod(data)
	case NPM:
		return parsePackageJSON(data)
	case PyPI:
		return parseRequirements(data)
	}
	return nil
}

// Added lists the names after declares that before did not, sorted.
func Added(eco string, before, after []byte) []string {
	had := map[string]bool{}
	for _, n := range Parse(eco, before) {
		had[n] = true
	}
	var out []string
	seen := map[string]bool{}
	for _, n := range Parse(eco, after) {
		if !had[n] && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// parseGoMod reads the require directives. An // indirect requirement is
// left out: go mod tidy adds those for what a direct one needs, and a
// hallucinated name is always a direct one.
func parseGoMod(data []byte) []string {
	var out []string
	inBlock := false
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.Contains(line, "// indirect") {
			continue
		}
		if i := strings.Index(line, "//"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		switch {
		case line == "require (":
			inBlock = true
			continue
		case inBlock && line == ")":
			inBlock = false
			continue
		case strings.HasPrefix(line, "require "):
			line = strings.TrimSpace(strings.TrimPrefix(line, "require "))
		case !inBlock:
			continue
		}
		if f := strings.Fields(line); len(f) >= 2 {
			out = append(out, f[0])
		}
	}
	return out
}

// parsePackageJSON reads the four dependency maps. A dependency that is
// not from the registry — a path, a workspace, a git URL, a tarball — is
// left out: there is nothing to look up.
func parsePackageJSON(data []byte) []string {
	var pkg map[string]json.RawMessage
	if json.Unmarshal(data, &pkg) != nil {
		return nil
	}
	var out []string
	for _, key := range []string{"dependencies", "devDependencies", "peerDependencies", "optionalDependencies"} {
		var deps map[string]string
		if json.Unmarshal(pkg[key], &deps) != nil {
			continue
		}
		for name, spec := range deps {
			if notRegistry(spec) {
				continue
			}
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func notRegistry(spec string) bool {
	for _, p := range []string{"file:", "link:", "workspace:", "portal:", "git", "http:", "https:", "github:"} {
		if strings.HasPrefix(spec, p) {
			return true
		}
	}
	return strings.Contains(spec, "/") && !strings.HasPrefix(spec, "npm:")
}

var reqName = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9._-]*)`)

// parseRequirements reads a pip requirements file: one name per line,
// with options, includes, URLs and editable installs left out. Names
// are normalised the way PyPI compares them.
func parseRequirements(data []byte) []string {
	var out []string
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if i := strings.Index(line, " #"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "-") || strings.Contains(line, "://") {
			continue
		}
		if m := reqName.FindString(line); m != "" {
			out = append(out, normalizePy(m))
		}
	}
	return out
}

var pySep = regexp.MustCompile(`[-_.]+`)

func normalizePy(name string) string {
	return strings.ToLower(pySep.ReplaceAllString(name, "-"))
}

// Info is what a registry says about one name.
type Info struct {
	Exists bool
	// FirstPublished is when the name's first release appeared; zero when
	// the registry did not say.
	FirstPublished time.Time
}

// Registry answers for one ecosystem's names. An error means "could not
// tell", which is never a finding.
type Registry interface {
	Lookup(ctx context.Context, eco, name string) (Info, error)
}

// Finding is one added dependency worth a line.
type Finding struct {
	Eco, Name string
	// Missing says the registry does not know the name.
	Missing bool
	// Age is how long ago it was first published, when it is under
	// YoungAge.
	Age time.Duration
}

// Check looks up each added name and returns the ones to flag. skip, when
// set, names the ones not to look up (a private module). Lookups that
// fail are dropped, and so is everything left when ctx ends.
func Check(ctx context.Context, reg Registry, eco string, names []string, now time.Time, skip func(string) bool) []Finding {
	var out []Finding
	for _, n := range names {
		if ctx.Err() != nil {
			break
		}
		if skip != nil && skip(n) {
			continue
		}
		info, err := reg.Lookup(ctx, eco, n)
		if err != nil {
			continue
		}
		switch {
		case !info.Exists:
			out = append(out, Finding{Eco: eco, Name: n, Missing: true})
		case !info.FirstPublished.IsZero() && now.Sub(info.FirstPublished) < YoungAge:
			age := now.Sub(info.FirstPublished)
			if age < 0 {
				age = 0
			}
			out = append(out, Finding{Eco: eco, Name: n, Age: age})
		}
	}
	return out
}

// Note is the line the findings add to the edit's result, "" for none.
func Note(path string, fs []Finding) string {
	if len(fs) == 0 {
		return ""
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "[dependency check] %s adds:", filepath.Base(path))
	for _, f := range fs {
		if f.Missing {
			fmt.Fprintf(&sb, "\n- %s: not found in the public %s registry. A name that does not exist is often a misremembered or invented one; check the real package name before building on it (a private module would not be listed either).", f.Name, f.Eco)
			continue
		}
		days := int(f.Age.Hours() / 24)
		fmt.Fprintf(&sb, "\n- %s: first published %s ago. A name this new can be someone squatting on a commonly misremembered one; check it is the package you mean.", f.Name, plural(days, "day"))
	}
	return sb.String()
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
}
