// Package doctorfix is the repair half of the checkup. The doctor
// reports; this decides which of the things it reports are safe and
// obvious enough for a program to put right on its own, and puts them
// right when it is asked to.
//
// The line it draws is deliberate. A misspelled config key, a directory
// that is missing or that nobody can read, a login that expired months
// ago and cannot renew itself: each of these has exactly one repair, and
// nothing is lost if it turns out to have been unnecessary. Credentials,
// which provider to use, what the project's files should say — those
// have more than one answer, and the answer is the user's, so they are
// reported and never touched.
package doctorfix

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/cmdresult"
	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/fsutil"
	"github.com/xmonader/janetcode/internal/mcp"
	"github.com/xmonader/janetcode/internal/trust"
	"github.com/xmonader/janetcode/internal/verify"
)

// Env is where the checkup looks. A zero field means "there is nothing
// of this kind here", never "look in the default place": the tests run
// against a temporary home and must not be able to reach the real one.
type Env struct {
	// ConfigPath is the config file, whether or not it exists; its
	// directory is the config directory.
	ConfigPath string
	// Cwd is the project.
	Cwd string
	// Trust is the store that records approval of project files.
	Trust trust.Store
	// Tokens is the MCP OAuth token store.
	Tokens *mcp.TokenStore
	// Dirs are directories that must stay readable: the memory store,
	// the skill directories.
	Dirs []string
	// Now is the clock, for token expiry; zero means time.Now.
	Now time.Time
}

// Fix is one repair: what is wrong, and the single thing that puts it
// right.
type Fix struct {
	// ID names the kind of problem, so a test or a script can look for
	// one without matching prose.
	ID string
	// What says what would be done, in the imperative: it is printed
	// after "would fix: " and, once done, after "fixed: ".
	What string
	// Path is the file or directory concerned.
	Path string

	apply func() error
}

// Check finds every safe repair this machine needs. It changes nothing.
func Check(env Env) []Fix {
	var out []Fix
	out = append(out, configDirFix(env)...)
	out = append(out, configKeyFixes(env)...)
	out = append(out, verifyTrustFix(env)...)
	out = append(out, staleTokenFixes(env)...)
	out = append(out, dirPermFixes(env)...)
	out = append(out, configPermFix(env)...)
	return out
}

// Apply carries out every repair Check finds and returns what it did, in
// the same order. It is idempotent: a second call finds nothing to do,
// because each repair removes the condition that produced it.
func Apply(env Env) (done []string, errs []string) {
	for _, f := range Check(env) {
		if err := f.apply(); err != nil {
			// Named by what it was doing, not only by the file: two key
			// repairs in one config file produced two identical messages
			// that said nothing about which repair failed.
			errs = append(errs, fmt.Sprintf("could not %s: %v", f.What, err))
			continue
		}
		done = append(done, f.What)
	}
	return done, errs
}

// Run is the whole command: with fix false it lists what it would do,
// with fix true it does it and says exactly what changed.
func Run(env Env, fix bool) cmdresult.Result {
	if !fix {
		found := Check(env)
		if len(found) == 0 {
			return cmdresult.Result{Notes: []string{"nothing to fix"}}
		}
		var b strings.Builder
		for _, f := range found {
			fmt.Fprintf(&b, "would fix: %s\n", f.What)
		}
		return cmdresult.Result{Plain: true, Sheet: strings.TrimRight(b.String(), "\n"),
			Tips: []string{summary(len(found))}}
	}
	done, errs := Apply(env)
	res := cmdresult.Result{}
	if len(done) == 0 && len(errs) == 0 {
		res.Notes = append(res.Notes, "nothing to fix")
		return res
	}
	var b strings.Builder
	for _, d := range done {
		fmt.Fprintf(&b, "fixed: %s\n", d)
	}
	for _, e := range errs {
		fmt.Fprintf(&b, "%s\n", e)
	}
	res.Plain, res.Sheet = true, strings.TrimRight(b.String(), "\n")
	if len(errs) > 0 {
		res.Error = plural(len(errs), "repair") + " failed"
	}
	return res
}

// Summary is the one line the front-ends show when they are not fixing
// anything: "3 things could be fixed: janetcode --doctor --fix". It is
// empty when there is nothing to do.
func Summary(env Env) string { return summary(len(Check(env))) }

func summary(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("%s could be fixed: janetcode --doctor --fix", plural(n, "thing"))
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// configDirFix: the config directory is where the config file, the trust
// store, the token store and the global skills live. Without it every
// write fails, one at a time, with a different message.
func configDirFix(env Env) []Fix {
	if env.ConfigPath == "" {
		return nil
	}
	dir := filepath.Dir(env.ConfigPath)
	if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
		return nil
	} else if err == nil {
		return nil // something is there and is not a directory: not ours to move
	}
	return []Fix{{ID: "config-dir", Path: dir, What: "create the config directory " + dir,
		apply: func() error { return os.MkdirAll(dir, 0o700) }}}
}

// configKeyFixes: the config file is decoded with unknown fields
// refused, so one typo makes janetcode start with no configuration at
// all and a message about a field. When the typo is one or two letters
// from a real key, the repair is to spell it properly.
func configKeyFixes(env Env) []Fix {
	if env.ConfigPath == "" {
		return nil
	}
	data, err := os.ReadFile(env.ConfigPath)
	if err != nil {
		return nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil // not our kind of broken: report it, do not guess
	}
	known := map[string]bool{}
	for _, k := range config.Keys() {
		known[k] = true
	}
	var bad []string
	for k := range raw {
		if !known[k] {
			bad = append(bad, k)
		}
	}
	sort.Strings(bad)
	var out []Fix
	for _, k := range bad {
		want, ok := nearest(k, config.Keys())
		if !ok {
			continue // no obvious correction: the doctor reports it instead
		}
		if raw[want] != nil {
			continue // both spellings present; which one wins is a judgement
		}
		re := regexp.MustCompile(`"` + regexp.QuoteMeta(k) + `"(\s*):`)
		if len(re.FindAllIndex(data, -1)) != 1 {
			continue // more than one place to change is more than one decision
		}
		path, from, to := env.ConfigPath, k, want
		out = append(out, Fix{ID: "config-key", Path: path,
			What: fmt.Sprintf("rename the config key %q to %q in %s", from, to, path),
			apply: func() error {
				cur, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				fixed := re.ReplaceAll(cur, []byte(`"`+to+`"$1:`))
				// Through the atomic path, like every other write to the
				// config file: a crash mid-write here would leave a
				// truncated config, which is the class of damage this
				// package exists to repair.
				return fsutil.WriteFileAtomic(path, fixed, fsutil.PrivateFileMode)
			}})
	}
	return out
}

// nearest picks the known key a misspelling obviously meant: within an
// edit distance of two, and not ambiguous between two candidates.
func nearest(bad string, keys []string) (string, bool) {
	best, bestD, ties := "", 3, 0
	for _, k := range keys {
		d := distance(bad, k)
		switch {
		case d < bestD:
			best, bestD, ties = k, d, 1
		case d == bestD:
			ties++
		}
	}
	if best == "" || bestD > 2 || ties > 1 {
		return "", false
	}
	// A three-letter key is two edits from half the alphabet; ask for a
	// correction that keeps most of the word.
	if bestD*3 > len(best) {
		return "", false
	}
	return best, true
}

// distance is Levenshtein, on bytes: config keys are ASCII.
func distance(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(min(cur[j-1]+1, prev[j]+1), prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

// verifyTrustFix: a project that ships verification commands gets none
// of them until they are approved, and the usual reason they are not
// approved is that nobody knew they were there.
func verifyTrustFix(env Env) []Fix {
	if env.Cwd == "" || env.Trust.Path == "" {
		return nil
	}
	vc, hash, err := verify.Load(env.Cwd)
	if err != nil || len(vc.Commands) == 0 || hash == "" {
		return nil
	}
	key := trust.VerifyKey(env.Cwd)
	if env.Trust.Trusted(key, hash) {
		return nil
	}
	store := env.Trust
	return []Fix{{ID: "verify-untrusted", Path: filepath.Join(env.Cwd, verify.ProjectFile),
		What:  fmt.Sprintf("trust %s, so its checks run after every change: %s", verify.ProjectFile, strings.Join(vc.Commands, "; ")),
		apply: func() error { return store.Trust(key, hash) }}}
}

// staleTokenFixes: an MCP login that has expired and carries no refresh
// token is dead weight. It cannot renew itself, so every call to that
// server fails on it; forgetting it is what makes the next call ask for
// a fresh login.
func staleTokenFixes(env Env) []Fix {
	if env.Tokens == nil {
		return nil
	}
	all, err := env.Tokens.All()
	if err != nil {
		return nil
	}
	now := env.Now
	if now.IsZero() {
		now = time.Now()
	}
	var urls []string
	for u, t := range all {
		if t.RefreshToken != "" || t.Expiry.IsZero() || t.Expiry.After(now) {
			continue
		}
		urls = append(urls, u)
	}
	sort.Strings(urls)
	var out []Fix
	for _, u := range urls {
		url, store := u, env.Tokens
		out = append(out, Fix{ID: "mcp-token-stale", Path: url,
			What:  fmt.Sprintf("forget the expired MCP login for %s (it has no refresh token; /mcp auth logs in again)", url),
			apply: func() error { return store.Delete(url) }})
	}
	return out
}

// dirPermFixes: a memory or skills directory the owner cannot read or
// enter makes the feature look switched off rather than broken. The
// repair is to give the owner back what a directory of ours always has.
func dirPermFixes(env Env) []Fix {
	var out []Fix
	seen := map[string]bool{}
	for _, d := range env.Dirs {
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		fi, err := os.Stat(d)
		if err != nil || !fi.IsDir() {
			continue
		}
		mode := fi.Mode().Perm()
		if mode&0o500 == 0o500 {
			continue
		}
		dir, want := d, mode|0o700
		out = append(out, Fix{ID: "dir-unreadable", Path: dir,
			What:  fmt.Sprintf("make %s readable again (mode %04o → %04o)", dir, mode, want),
			apply: func() error { return os.Chmod(dir, want) }})
	}
	return out
}

// configPermFix makes a config file other users can read private again.
// It can hold API keys; janetcode writes it 0600, but one written by
// hand, copied in, or restored from a backup often is not.
func configPermFix(env Env) []Fix {
	if runtime.GOOS == "windows" || env.ConfigPath == "" {
		return nil
	}
	fi, err := os.Stat(env.ConfigPath)
	if err != nil || !fi.Mode().IsRegular() {
		return nil
	}
	mode := fi.Mode().Perm()
	if mode&0o077 == 0 {
		return nil
	}
	path, want := env.ConfigPath, mode&0o700|0o600
	return []Fix{{ID: "config-readable", Path: path,
		What:  fmt.Sprintf("make %s private (mode %04o → %04o): it can hold API keys", path, mode, want),
		apply: func() error { return os.Chmod(path, want) }}}
}
