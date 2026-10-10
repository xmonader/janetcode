package gaming

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestLiteralPrescreen is the offline pre-screen for the literal scan: it
// replays the scan on effbench workspaces a campaign left behind, each a
// git repository whose one commit is the seed, so the final diff is the
// work tree against HEAD. Nothing runs unless JANETCODE_PRESCREEN_WS
// names a directory of campaigns (effbench's /tmp/effbench-ws), and
// JANETCODE_PRESCREEN_GLOB narrows which (default "*"):
//
//	JANETCODE_PRESCREEN_WS=/tmp/effbench-ws JANETCODE_PRESCREEN_GLOB='jc-mimo-r13/*' \
//	  go test ./internal/gaming -run LiteralPrescreen -v
//
// Every run it scans was graded by a hidden suite, so any flag on a
// passing run is a false flag; the log says which.
func TestLiteralPrescreen(t *testing.T) {
	root := os.Getenv("JANETCODE_PRESCREEN_WS")
	if root == "" {
		t.Skip("JANETCODE_PRESCREEN_WS not set")
	}
	glob := os.Getenv("JANETCODE_PRESCREEN_GLOB")
	if glob == "" {
		glob = "*/*"
	}
	arms, _ := filepath.Glob(filepath.Join(root, glob))
	runs, flagged, flags := 0, 0, 0
	for _, arm := range arms {
		tasks, _ := filepath.Glob(filepath.Join(arm, "*", "workspace"))
		for _, ws := range tasks {
			sigs, ok := prescreenOne(ws)
			if !ok {
				continue
			}
			runs++
			if len(sigs) > 0 {
				flagged++
				flags += len(sigs)
				rel, _ := filepath.Rel(root, filepath.Dir(ws))
				for _, s := range sigs {
					t.Logf("FLAG %s: %s", rel, s)
				}
			}
		}
	}
	t.Logf("literal prescreen: %d runs scanned, %d flagged, %d flags", runs, flagged, flags)
}

func prescreenOne(ws string) ([]Signal, bool) {
	git := func(args ...string) (string, bool) {
		out, err := exec.Command("git", append([]string{"-C", ws}, args...)...).Output()
		return string(out), err == nil
	}
	tracked, ok := git("ls-files")
	if !ok {
		return nil, false
	}
	tests := map[string]string{}
	spec := strings.Builder{}
	for _, p := range strings.Fields(tracked) {
		switch {
		case isTestPath(p):
			if c, ok := git("show", "HEAD:"+p); ok {
				tests[p] = c
			}
		case strings.HasSuffix(p, ".md"):
			if c, ok := git("show", "HEAD:"+p); ok {
				spec.WriteString(c + "\n")
			}
		}
	}
	changed, _ := git("diff", "--name-only", "HEAD")
	untracked, _ := git("ls-files", "--others", "--exclude-standard")
	var sources []Source
	paths := append(strings.Fields(changed), strings.Fields(untracked)...)
	sort.Strings(paths)
	for _, p := range paths {
		if isTestPath(p) || !sourceExt(p) {
			continue
		}
		before, _ := git("show", "HEAD:"+p)
		s := Source{Path: p, Before: before}
		if d, ok := git("diff", "-U0", "HEAD", "--", p); ok && d != "" {
			for _, l := range strings.Split(d, "\n") {
				if strings.HasPrefix(l, "+") && !strings.HasPrefix(l, "+++") {
					s.Added = append(s.Added, l[1:])
				}
			}
		} else if data, err := os.ReadFile(filepath.Join(ws, p)); err == nil {
			s.Added = strings.Split(string(data), "\n")
		}
		sources = append(sources, s)
	}
	return LiteralBranches(TestLiterals(tests), spec.String(), sources), true
}

func sourceExt(p string) bool {
	switch filepath.Ext(p) {
	case ".py", ".go", ".rs", ".ts", ".js", ".mjs", ".java", ".rb":
		return true
	}
	return false
}
