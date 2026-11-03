package tool

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/shell"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// A bash command that is a plain grep or rg and finds nothing gets the
// same empty-search probes the grep tool runs (see EmptyProbe): the
// model searches through bash far more often than through grep.

const (
	bashProbeTimeout = 5 * time.Second
	bashProbeBytes   = 64 << 10
)

// searchProbe is one variant of an empty search: what it changed and the
// argv to run in place of the original.
type searchProbe struct {
	what string
	argv []string
}

// searchArgv recognises a command that is nothing but a search: one grep,
// egrep, fgrep, rg or git grep, optionally piped once into head or tail
// with a line count. It returns the search's argv, and whether a pipe
// followed it. Anything the shell would treat specially (other pipes,
// lists, redirections, substitutions, globs, escapes, comments) is not a
// plain search.
func searchArgv(command string) (argv []string, piped bool, ok bool) {
	command = strings.TrimSpace(command)
	pipe := -1
	quote := byte(0)
	for i := 0; i < len(command); i++ {
		c := command[i]
		switch {
		case quote == '\'':
			if c == '\'' {
				quote = 0
			}
		case quote == '"':
			switch c {
			case '"':
				quote = 0
			case '$', '`', '\\':
				return nil, false, false
			}
		case c == '\'' || c == '"':
			quote = c
		case c == '|':
			if pipe >= 0 {
				return nil, false, false
			}
			pipe = i
		case strings.IndexByte(";&<>()`$*?[]{}~\\\n\r#!", c) >= 0:
			return nil, false, false
		}
	}
	if quote != 0 {
		return nil, false, false
	}
	left := command
	if pipe >= 0 {
		left = command[:pipe]
		if !headTailWords(shell.Words(command[pipe+1:])) {
			return nil, false, false
		}
	}
	argv = shell.Words(left)
	if len(argv) < 2 {
		return nil, false, false
	}
	switch argv[0] {
	case "grep", "egrep", "fgrep", "rg":
	case "git":
		if argv[1] != "grep" {
			return nil, false, false
		}
	default:
		return nil, false, false
	}
	return argv, pipe >= 0, true
}

// headTailWords reports whether w is head or tail with at most a line
// count: -n N, -nN or -N.
func headTailWords(w []string) bool {
	if len(w) == 0 || (w[0] != "head" && w[0] != "tail") {
		return false
	}
	num := func(s string) bool { _, err := strconv.Atoi(s); return err == nil }
	switch len(w) {
	case 1:
		return true
	case 2:
		return (strings.HasPrefix(w[1], "-n") && num(w[1][2:])) || (len(w[1]) > 1 && w[1][0] == '-' && num(w[1][1:]))
	case 3:
		return w[1] == "-n" && num(w[2])
	}
	return false
}

// Options that take their value as the next word when given alone.
var (
	grepValueOpts = map[string]bool{"-A": true, "-B": true, "-C": true, "-m": true, "-d": true, "-D": true,
		"--include": true, "--exclude": true, "--exclude-dir": true, "--exclude-from": true,
		"--max-count": true, "--context": true, "--after-context": true, "--before-context": true,
		"--directories": true, "--devices": true, "--label": true}
	rgValueOpts = map[string]bool{"-A": true, "-B": true, "-C": true, "-m": true, "-g": true, "-t": true, "-T": true,
		"-j": true, "-M": true, "-E": true, "-r": true,
		"--glob": true, "--iglob": true, "--type": true, "--type-not": true, "--max-count": true,
		"--context": true, "--after-context": true, "--before-context": true, "--threads": true,
		"--max-columns": true, "--encoding": true, "--replace": true, "--max-depth": true}
)

// searchPattern returns the index in argv of the pattern word (the first
// non-option, or the value of -e/--regexp) and the index of the first
// word after the command. pat -1 means it could not be told confidently.
func searchPattern(argv []string) (pat int, start int) {
	start = 1
	if argv[0] == "git" {
		start = 2
	}
	vals := grepValueOpts
	if argv[0] == "rg" {
		vals = rgValueOpts
	}
	for i := start; i < len(argv); i++ {
		a := argv[i]
		switch {
		case a == "--":
			if i+1 < len(argv) {
				return i + 1, start
			}
			return -1, start
		case a == "-e" || a == "--regexp":
			if i+1 < len(argv) {
				return i + 1, start
			}
			return -1, start
		case strings.HasPrefix(a, "--regexp="):
			return i, start
		case a == "--file" || strings.HasPrefix(a, "--file="):
			return -1, start
		case strings.HasPrefix(a, "--"):
			if vals[a] {
				i++
			}
		case strings.HasPrefix(a, "-") && len(a) > 1:
			for j := 1; j < len(a); j++ {
				c := a[j]
				if c == 'f' {
					return -1, start
				}
				if c == 'e' {
					if j == len(a)-1 && i+1 < len(argv) {
						return i + 1, start
					}
					return -1, start
				}
				if vals["-"+string(c)] {
					if j == len(a)-1 {
						i++
					}
					break
				}
			}
		default:
			return i, start
		}
	}
	return -1, start
}

// hasFlag reports whether argv (before any "--") carries one of the
// flags, as a word of its own or as a letter of a short cluster.
func hasFlag(argv []string, flags ...string) bool {
	for _, a := range argv[1:] {
		if a == "--" {
			return false
		}
		for _, f := range flags {
			if a == f {
				return true
			}
		}
		if len(a) > 2 && a[0] == '-' && a[1] != '-' {
			for _, f := range flags {
				if len(f) == 2 && f[0] == '-' && f[1] != '-' && strings.IndexByte(a[1:], f[1]) >= 0 {
					return true
				}
			}
		}
	}
	return false
}

// stripSearchFilters removes the INCLUSION filters of a grep or rg argv
// (--include, rg -g/--glob/--iglob without a leading "!", -t/--type) and
// reports whether there were any. Exclusions (--exclude*, rg -g '!...',
// -T/--type-not) stay, so text from files the search deliberately skips
// (.env, say) never shows up in the examples.
func stripSearchFilters(argv []string) ([]string, bool) {
	rg := argv[0] == "rg"
	out := make([]string, 0, len(argv))
	removed := false
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		if a == "--" {
			out = append(out, argv[i:]...)
			break
		}
		name, val, hasEq := strings.Cut(a, "=")
		value := val
		if !hasEq && i+1 < len(argv) {
			value = argv[i+1]
		}
		globFlag := rg && (name == "--glob" || name == "--iglob" || a == "-g")
		switch {
		case i == 0:
			out = append(out, a)
		case globFlag && strings.HasPrefix(value, "!"):
			// An exclusion: kept, with its value word.
			out = append(out, a)
			if !hasEq && i+1 < len(argv) {
				i++
				out = append(out, argv[i])
			}
		case !rg && argv[0] != "git" && name == "--include",
			globFlag, rg && (name == "--type" || a == "-t"):
			removed = true
			if !hasEq {
				i++
			}
		default:
			out = append(out, a)
		}
	}
	return out, removed
}

// unsafeProbeFlag reports whether argv carries an option that runs a
// program or reads inputs beyond the search itself, which a probe must not
// repeat: rg --pre/--pre-glob/-z/--search-zip, git grep -O/--open-files-in-pager.
func unsafeProbeFlag(argv []string) bool {
	for _, a := range argv[1:] {
		if a == "--" {
			return false
		}
		name, _, _ := strings.Cut(a, "=")
		switch name {
		case "--pre", "--pre-glob", "--search-zip", "--open-files-in-pager":
			return true
		}
		if len(a) > 1 && a[0] == '-' && a[1] != '-' {
			if argv[0] == "git" && strings.Contains(a, "O") {
				return true
			}
			if argv[0] == "rg" && strings.Contains(a, "z") {
				return true
			}
		}
	}
	return false
}

// searchProbes lists the variants to try, in order, for an empty search:
// ignoring case, as a literal string (when the pattern has regex
// metacharacters), without file filters. Pure: argv in, argvs out. nil
// when the argv is not one it can vary with confidence.
func searchProbes(argv []string) []searchProbe {
	// Only exactly "git grep ..." (not "git -c x=y grep"), and no option
	// that runs a program.
	if len(argv) < 2 || (argv[0] == "git" && (len(argv) < 3 || argv[1] != "grep")) || unsafeProbeFlag(argv) {
		return nil
	}
	pat, start := searchPattern(argv)
	if pat < 0 || hasFlag(argv, "-q", "-c", "-v", "-L", "--quiet", "--count", "--files") {
		return nil
	}
	with := func(flag string) []string {
		out := append([]string{}, argv[:start]...)
		out = append(out, flag)
		return append(out, argv[start:]...)
	}
	var ps []searchProbe
	if !hasFlag(argv, "-i", "-y", "--ignore-case") {
		ps = append(ps, searchProbe{"Case-insensitive, the same search", with("-i")})
	}
	p := strings.TrimPrefix(argv[pat], "--regexp=")
	if strings.ContainsAny(p, `.*+?()[]{}|^$\`) && argv[0] != "fgrep" &&
		!hasFlag(argv, "-F", "-P", "--fixed-strings", "--perl-regexp") {
		ps = append(ps, searchProbe{"As a literal string, the same search", with("-F")})
	}
	if stripped, ok := stripSearchFilters(argv); ok {
		ps = append(ps, searchProbe{"Without its file filters, the same search", stripped})
	}
	return ps
}

// probeSearch runs the probes for an empty bash search directly (no
// shell, stdout capped, stderr dropped) and returns the note for the
// first that finds lines. Each probe takes one unit of the shared budget.
func (t Bash) probeSearch(ctx context.Context, root string, argv []string) string {
	if t.Probe == nil {
		return ""
	}
	for _, p := range searchProbes(argv) {
		bin, err := exec.LookPath(p.argv[0])
		if err != nil {
			continue
		}
		if !t.Probe.take() {
			return ""
		}
		n, ex := runProbe(ctx, root, bin, p.argv)
		if n == 0 {
			continue
		}
		cnt := strconv.Itoa(n)
		if n >= maxProbeMatches {
			cnt = strconv.Itoa(maxProbeMatches) + "+"
		}
		return fmt.Sprintf("\n[empty search probe] %s matches %s lines, e.g.:\n  %s", p.what, cnt, strings.Join(ex, "\n  "))
	}
	return ""
}

func runProbe(ctx context.Context, root, bin string, argv []string) (int, []string) {
	ctx, cancel := context.WithTimeout(ctx, bashProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, argv[1:]...)
	cmd.Dir = root
	cmd.Env = shell.ScrubEnv(os.Environ())
	shell.SetProcessGroup(cmd)
	pipe, err := cmd.StdoutPipe()
	if err != nil || cmd.Start() != nil {
		return 0, nil
	}
	n := 0
	var ex []string
	sc := bufio.NewScanner(io.LimitReader(pipe, bashProbeBytes))
	sc.Buffer(make([]byte, 0, 16<<10), 1<<20)
	for sc.Scan() && n < maxProbeMatches {
		n++
		if len(ex) < maxProbeShown {
			ex = append(ex, textfmt.ClipBytes(strings.TrimSpace(sc.Text()), maxProbeLine))
		}
	}
	cancel()
	cmd.Wait()
	return n, ex
}
