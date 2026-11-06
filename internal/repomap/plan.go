package repomap

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// Plan verification: before the model acts on a plan, the names it wrote
// in backticks are checked against the repository. A file that does not
// exist or a symbol that is neither defined nor used anywhere is
// reported, unless the sentence says the model means to create it.

var (
	backtickRe   = regexp.MustCompile("`([^`\n]{2,120})`")
	symbolRe     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.]*$`)
	planFileExt  = regexp.MustCompile(`\.(go|py|pyi|ts|tsx|js|jsx|mjs|rs|java|kt|rb|c|h|cc|cpp|hpp|cs|swift|md|json|yaml|yml|toml|txt|sh|mod|sum|html|css|sql|proto)$`)
	createWord   = regexp.MustCompile(`(?i)\b(add|adds|adding|create|creates|creating|new|introduce|introducing|define|defines|defining|write|writes|writing|implement|implements|implementing|generate|generating|rename\w*|move\w*|extract\w*|install\w*)\b`)
	planPosition = regexp.MustCompile(`(:\d+(-\d+)?){1,2}$`)
	maxPlanItems = 5
)

// Known reports whether a symbol name is defined in the repository, or
// used by any file in it (an external API the code already calls).
func (ix *Index) Known(name string) bool {
	if len(ix.definers[name]) > 0 {
		return true
	}
	for def := range ix.definers {
		if i := strings.IndexByte(def, '.'); i >= 0 && def[i+1:] == name {
			return true
		}
	}
	for _, f := range ix.Files {
		if f.refs[name] > 0 {
			return true
		}
	}
	return false
}

// hasGo reports whether the index holds Go source.
func (ix *Index) hasGo() bool {
	for _, f := range ix.Files {
		if strings.HasSuffix(f.Path, ".go") {
			return true
		}
	}
	return false
}

// definedInGo reports whether a Go file defines name. Only Go code can
// own a Go selector: hugo's minified livereload.js defines `re`, and
// `re.ExpandString` was reported missing because of it.
func (ix *Index) definedInGo(name string) bool {
	for _, i := range ix.definers[name] {
		if strings.HasSuffix(ix.Files[i].Path, ".go") {
			return true
		}
	}
	return false
}

// hasPathSuffix reports whether some indexed file is p or ends in /p.
func (ix *Index) hasPathSuffix(p string) bool {
	p = strings.TrimPrefix(p, "/")
	for _, f := range ix.Files {
		if f.Path == p || strings.HasSuffix(f.Path, "/"+p) {
			return true
		}
	}
	return false
}

// CheckPlan reads the backticked names in text and returns a note about
// the ones the repository does not have, or "" when every name checks
// out. Sentences that say the name is to be added, created or written
// are not checked: the plan is allowed to introduce things.
func (c *Cache) CheckPlan(ctx context.Context, text string) string {
	if !strings.Contains(text, "`") {
		return ""
	}
	ix, err := c.Get(ctx)
	if err != nil {
		return ""
	}
	var missing []string
	seen := map[string]bool{}
	for _, sentence := range splitSentences(text) {
		if createWord.MatchString(sentence) {
			continue
		}
		for _, m := range backtickRe.FindAllStringSubmatch(sentence, -1) {
			span := strings.TrimSpace(m[1])
			if i := strings.IndexByte(span, '('); i > 0 {
				span = span[:i] // a call: check the callee
			}
			if seen[span] {
				continue
			}
			switch {
			case looksLikePath(span):
				p := strings.TrimPrefix(span, "./")
				// path:line, path:line:col, path:a-b. All of the position
				// goes: cutting only the last part left "path:line" of a
				// position with a column, a file that never exists.
				if loc := planPosition.FindStringIndex(p); loc != nil && loc[0] > 0 {
					p = p[:loc[0]]
				} else if i := strings.LastIndexByte(p, ':'); i > 0 {
					p = p[:i]
				}
				if _, ok := ix.byPath[filepath.ToSlash(p)]; ok {
					continue
				}
				abs := p
				if !filepath.IsAbs(abs) {
					abs = filepath.Join(ix.Root, p)
				} else if r, err := filepath.Rel(ix.Root, abs); err != nil || strings.HasPrefix(r, "..") {
					// Outside the repository: a scratch file in /tmp or a
					// URL path such as `/foo%2f`, not a name the plan
					// gets wrong about this code.
					continue
				}
				if _, err := os.Stat(abs); err == nil {
					continue
				}
				// A name written without its directory ("cache.py" for
				// reports/cache.py) is how plans name files; it is a
				// file the repository has, not a missing one.
				if ix.hasPathSuffix(filepath.ToSlash(p)) {
					continue
				}
				seen[span] = true
				missing = append(missing, fmt.Sprintf("`%s` does not exist", span))
			case looksLikeSymbol(span):
				name, owner := span, ""
				if i := strings.LastIndexByte(name, '.'); i >= 0 {
					name, owner = name[i+1:], name[:i]
				}
				if len(name) < 3 || ix.Known(name) {
					continue
				}
				// Standard-library API, unless it is named as a member of
				// one of the repository's own Go types (`Handler.Flush`).
				if ix.hasGo() && !ix.definedInGo(owner) && goStdName(name) {
					continue
				}
				seen[span] = true
				missing = append(missing, fmt.Sprintf("`%s` is neither defined nor used anywhere in the repository", span))
			}
		}
	}
	if len(missing) == 0 {
		return ""
	}
	sort.Strings(missing)
	if len(missing) > maxPlanItems {
		missing = append(missing[:maxPlanItems], fmt.Sprintf("%d more", len(missing)-maxPlanItems))
	}
	return "plan check: " + strings.Join(missing, "; ") + ". If you mean to create them, go ahead; otherwise verify the names before building on them."
}

// splitSentences splits on line breaks and sentence-ending periods
// (a period followed by a space), keeping dotted names whole.
func splitSentences(text string) []string {
	var out []string
	start := 0
	for i := 0; i < len(text); i++ {
		if text[i] == '\n' || text[i] == '.' && i+1 < len(text) && text[i+1] == ' ' {
			out = append(out, text[start:i])
			start = i + 1
		}
	}
	return append(out, text[start:])
}

func looksLikePath(s string) bool {
	// A template's output (`[||a|a/b/c]`), a glob or an assignment
	// carries slashes too, but no file is named like that.
	if strings.ContainsAny(s, " \t{}()<>\"'|[]*?=,;$&!") {
		return false
	}
	return strings.Contains(s, "/") || planFileExt.MatchString(s)
}

// looksLikeSymbol accepts identifiers that read as code names rather
// than words: CamelCase, snake_case, or dotted (pkg.Name), at least four
// characters, not all lowercase letters.
func looksLikeSymbol(s string) bool {
	if len(s) < 4 || !symbolRe.MatchString(s) {
		return false
	}
	hasUpper, hasSep := false, strings.ContainsAny(s, "._")
	for _, r := range s {
		if unicode.IsUpper(r) {
			hasUpper = true
		}
	}
	return hasUpper || hasSep
}
