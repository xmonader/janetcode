package webui

import (
	"context"
	"fmt"
	"net/http"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/gitx"
)

// The empty page's suggestions, read from the project rather than written
// for every project: the changes not yet committed, a TODO the code
// carries, the last commit, a Go package with no tests. Each comes from one
// bounded git call; one that fails or is slow is left out, and what is
// missing is made up from general suggestions, so the page always has some.

type suggestion struct {
	Label  string `json:"label"`
	Prompt string `json:"prompt"`
}

// maxSuggestions is how many chips the empty page shows.
const maxSuggestions = 4

// suggestTimeout bounds each git call; a huge repository must not hold
// the empty page.
const suggestTimeout = 2 * time.Second

var generalSuggestions = []suggestion{
	{"Explain this project", "Explain how this project is structured"},
	{"Find and fix a bug", "Find a bug and fix it"},
	{"Add missing tests", "Add tests for the most important untested code"},
	{"Refactor something ugly", "Refactor the ugliest function you can find"},
}

func (s *server) suggestAPI(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, projectSuggestions(r.Context(), s.cwd()))
}

// projectSuggestions is the chips for dir: the project's own first, then
// general ones, maxSuggestions in all.
func projectSuggestions(ctx context.Context, dir string) []suggestion {
	var out []suggestion
	if dir != "" {
		for _, f := range []func(context.Context, string) (suggestion, bool){
			suggestReview, suggestTodo, suggestLastCommit, suggestUntested,
		} {
			if sg, ok := f(ctx, dir); ok {
				out = append(out, sg)
			}
		}
	}
	for _, g := range generalSuggestions {
		if len(out) >= maxSuggestions {
			break
		}
		if !hasLabel(out, g.Label) {
			out = append(out, g)
		}
	}
	return out[:min(len(out), maxSuggestions)]
}

func hasLabel(ss []suggestion, label string) bool {
	for _, s := range ss {
		if s.Label == label {
			return true
		}
	}
	return false
}

func gitOut(ctx context.Context, dir string, args ...string) (string, bool) {
	ctx, cancel := context.WithTimeout(ctx, suggestTimeout)
	defer cancel()
	out, err := gitx.Run(ctx, dir, nil, args...)
	return out, err == nil
}

// suggestReview offers a review of the working tree when it has changes.
func suggestReview(ctx context.Context, dir string) (suggestion, bool) {
	out, ok := gitOut(ctx, dir, "status", "--porcelain", "--untracked-files=no")
	if !ok {
		return suggestion{}, false
	}
	n := 0
	for _, l := range strings.Split(out, "\n") {
		if strings.TrimSpace(l) != "" {
			n++
		}
	}
	if n == 0 {
		return suggestion{}, false
	}
	files := "file"
	if n > 1 {
		files = "files"
	}
	return suggestion{
		Label:  fmt.Sprintf("Review my %d changed %s", n, files),
		Prompt: "Review my uncommitted changes (git diff HEAD) for bugs and missing tests, then propose a commit message",
	}, true
}

// suggestTodo offers the first TODO or FIXME in the tracked source, written
// as a marker (TODO: or TODO(who):) in a comment, not the word in prose.
func suggestTodo(ctx context.Context, dir string) (suggestion, bool) {
	out, ok := gitOut(ctx, dir, "grep", "-n", "-I", "-m", "1", "-E", `(//|#|--|/\*)\s*(TODO|FIXME)(\([^)]*\))?:`,
		"--", ".", ":!vendor", ":!node_modules", ":!*.min.js", ":!third_party")
	if !ok {
		return suggestion{}, false
	}
	first, _, _ := strings.Cut(out, "\n")
	// path:line:text
	parts := strings.SplitN(first, ":", 3)
	if len(parts) != 3 {
		return suggestion{}, false
	}
	file, line, text := parts[0], parts[1], strings.TrimSpace(parts[2])
	text = clip(text, 160)
	return suggestion{
		Label:  "Resolve a TODO in " + clip(path.Base(file), 24),
		Prompt: fmt.Sprintf("Resolve the TODO at %s:%s (%s)", file, line, text),
	}, true
}

// suggestLastCommit offers a second look at the last commit.
func suggestLastCommit(ctx context.Context, dir string) (suggestion, bool) {
	out, ok := gitOut(ctx, dir, "log", "-1", "--format=%h %s")
	out = strings.TrimSpace(out)
	if !ok || out == "" {
		return suggestion{}, false
	}
	sha, subject, _ := strings.Cut(out, " ")
	return suggestion{
		Label:  "Check the last commit",
		Prompt: fmt.Sprintf("Explain what commit %s (%q) changed and check it for mistakes", sha, clip(subject, 120)),
	}, true
}

// suggestUntested offers tests for the largest Go package that has none.
func suggestUntested(ctx context.Context, dir string) (suggestion, bool) {
	out, ok := gitOut(ctx, dir, "ls-files", "--", "*.go", ":!vendor", ":!testdata")
	if !ok {
		return suggestion{}, false
	}
	src, tested := map[string]int{}, map[string]bool{}
	for _, f := range strings.Split(out, "\n") {
		if f == "" || strings.Contains(f, "/testdata/") {
			continue
		}
		d := path.Dir(f)
		if strings.HasSuffix(f, "_test.go") {
			tested[d] = true
		} else {
			src[d]++
		}
	}
	var dirs []string
	for d := range src {
		if !tested[d] && !helperPackage(d) {
			dirs = append(dirs, d)
		}
	}
	if len(dirs) == 0 {
		return suggestion{}, false
	}
	sort.Slice(dirs, func(i, j int) bool {
		if src[dirs[i]] != src[dirs[j]] {
			return src[dirs[i]] > src[dirs[j]]
		}
		return dirs[i] < dirs[j]
	})
	d, name := dirs[0], "the root package"
	if d != "." {
		name = clip(path.Base(d), 24)
	}
	return suggestion{
		Label:  "Add tests to " + name,
		Prompt: fmt.Sprintf("The Go package in %s has no tests. Add tests for its most important behaviour", d),
	}, true
}

// helperPackage reports a package that exists for tests or examples, which
// wanting tests of its own would be a poor suggestion.
func helperPackage(d string) bool {
	b := strings.ToLower(path.Base(d))
	return strings.HasSuffix(b, "test") || strings.HasSuffix(b, "testutil") || strings.HasSuffix(b, "testing") ||
		b == "mocks" || b == "mock" || b == "fakes" || b == "examples" || b == "example"
}

// clip cuts s to n runes with an ellipsis.
func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
