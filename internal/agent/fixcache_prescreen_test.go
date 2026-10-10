package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/fixcache"
	"github.com/xmonader/janetcode/internal/perm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/shell"
)

// TestFixCachePrescreen is the zero-token pre-screen for the error-signature
// fix cache (fixcache.go): over recorded sessions, how often a red check's
// error signature recurs in a later session of the same repository, and
// how often the earlier session had already resolved it, which is the only
// case the cache attaches anything.
//
// It reads directories of session logs, so it is skipped unless
// JANETCODE_FIXCACHE_PRESCREEN names them (a path list):
//
//	JANETCODE_FIXCACHE_PRESCREEN=docs/benchmarks/effbench/runs:$HOME/.local/share/janetcode/sessions go test ./internal/agent -run FixCachePrescreen -v
//
// An effbench workspace is a fresh copy of one task's repository per run,
// so its repository is the task (the run directory without its seed): a
// recurrence there is the same task replayed, which says the signatures
// are stable, not that real projects repeat their errors. The real
// sessions are reported separately for that reason.
func TestFixCachePrescreen(t *testing.T) {
	dirs := os.Getenv("JANETCODE_FIXCACHE_PRESCREEN")
	if dirs == "" {
		t.Skip("set JANETCODE_FIXCACHE_PRESCREEN to directories of session logs")
	}
	type rec struct {
		repo, kind string
		s          *session.Session
	}
	var all []rec
	for _, root := range filepath.SplitList(dirs) {
		_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".jsonl") {
				return nil
			}
			s, _, err := session.Open(filepath.Dir(p), strings.TrimSuffix(filepath.Base(p), ".jsonl"))
			if err != nil || s.Meta.Cwd == "" {
				return nil
			}
			repo, kind := fixcachePrescreenRepo(s.Meta.Cwd)
			all = append(all, rec{repo, kind, s})
			return nil
		})
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].s.Meta.Created.Before(all[j].s.Meta.Created) })
	type tally struct {
		sessions, reds, signed, recurring, hits, hitsOverlap int
		sigSessions                                          map[string]map[string]bool
		examples                                             []string
	}
	tallies := map[string]*tally{}
	stores := map[string]*fixcache.Store{}
	for _, r := range all {
		tt := tallies[r.kind]
		if tt == nil {
			tt = &tally{sigSessions: map[string]map[string]bool{}}
			tallies[r.kind] = tt
		}
		tt.sessions++
		st := stores[r.repo]
		if st == nil {
			st = &fixcache.Store{}
			stores[r.repo] = st
		}
		for _, x := range fixcachePrescreenSession(r.s) {
			tt.reds++
			if len(x.sigs) == 0 {
				continue
			}
			tt.signed++
			for _, g := range x.sigs {
				k := r.repo + "\x00" + g
				if tt.sigSessions[k] == nil {
					tt.sigSessions[k] = map[string]bool{}
				}
				tt.sigSessions[k][r.s.Meta.ID] = true
			}
			if e, shared, ok := st.Lookup(x.sigs, r.s.Meta.ID); ok {
				tt.hits++
				if fixcacheOverlap(e.Files, x.fixFiles) {
					tt.hitsOverlap++
				}
				if len(tt.examples) < 6 {
					tt.examples = append(tt.examples, fmt.Sprintf("%s: %s", filepath.Base(r.repo), shared))
				}
			}
		}
		// The session's resolutions go in only after it is scanned: a
		// hit is an earlier session's fix.
		for _, x := range fixcachePrescreenSession(r.s) {
			if x.resolved {
				st.Add(fixcache.Entry{Signatures: x.sigs, Fix: "x", Files: x.fixFiles, Session: r.s.Meta.ID})
			}
		}
	}
	for _, kind := range []string{"effbench", "real"} {
		tt := tallies[kind]
		if tt == nil {
			t.Logf("%s: no sessions", kind)
			continue
		}
		recurring, distinct := 0, len(tt.sigSessions)
		for _, ss := range tt.sigSessions {
			if len(ss) > 1 {
				recurring++
			}
		}
		pct := func(n, d int) string {
			if d == 0 {
				return "n/a"
			}
			return fmt.Sprintf("%.1f%%", 100*float64(n)/float64(d))
		}
		t.Logf("%s: sessions %d, red episodes %d, with a signature %d; distinct (repo, signature) %d, in 2+ sessions %d (%s); red episodes an earlier session's resolved fix matches %d (%s of signed reds), of which the fix touched a file this session's own fix touched %d",
			kind, tt.sessions, tt.reds, tt.signed, distinct, recurring, pct(recurring, distinct), tt.hits, pct(tt.hits, tt.signed), tt.hitsOverlap)
		if len(tt.examples) > 0 {
			t.Logf("  e.g. %s", strings.Join(tt.examples, " | "))
		}
	}
}

var fixcacheEffbenchRe = regexp.MustCompile(`/effbench-ws/[^/]+/[^/]+/(.+?)-s\d+/workspace`)

// fixcachePrescreenRepo is the repository a session's directory belongs to
// and the kind of session it was.
func fixcachePrescreenRepo(cwd string) (repo, kind string) {
	if m := fixcacheEffbenchRe.FindStringSubmatch(cwd); m != nil {
		return "effbench:" + m[1], "effbench"
	}
	return cwd, "real"
}

// fixcacheRed is one red check of a session: its signatures, whether a
// later run of the same command with an edit in between went green, and
// the files edited in between.
type fixcacheRed struct {
	sigs     []string
	resolved bool
	fixFiles []string
}

func fixcachePrescreenSession(s *session.Session) []fixcacheRed {
	root := s.Meta.Cwd
	var reds []fixcacheRed
	type pend struct {
		i     int
		files map[string]bool
	}
	pending := map[string]*pend{}
	for _, e := range s.Events() {
		if e.Type != session.TypeToolResult || refused(e) {
			continue
		}
		if editTool(e.Name) && !e.IsError {
			p := perm.Subject(e.Name, []byte(e.Arguments))
			if r, err := filepath.Rel(root, p); err == nil && !strings.HasPrefix(r, "..") {
				p = r
			}
			for _, pd := range pending {
				pd.files[p] = true
			}
			continue
		}
		if e.Name != "bash" || neverRan(e) {
			continue
		}
		cmd := shell.TrimWorkspaceCd(shell.CommandOf(e.Arguments), root)
		if !shell.IsCheck(cmd) {
			continue
		}
		if runFailed(e) {
			if pending[cmd] == nil {
				reds = append(reds, fixcacheRed{sigs: fixcache.Signatures(e.Output, root)})
				pending[cmd] = &pend{i: len(reds) - 1, files: map[string]bool{}}
			}
			continue
		}
		if pd := pending[cmd]; pd != nil {
			if len(pd.files) > 0 {
				reds[pd.i].resolved = true
				for f := range pd.files {
					reds[pd.i].fixFiles = append(reds[pd.i].fixFiles, f)
				}
				sort.Strings(reds[pd.i].fixFiles)
			}
			delete(pending, cmd)
		}
	}
	return reds
}

func fixcacheOverlap(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}
	return false
}
