package shell

import (
	"cmp"
	"os"
	"os/user"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
)

// Risk grades a shell command. The threat model is narrow on purpose:
// commands that destroy the machine are refused outright, commands that
// change files outside the workspace always ask, everything else follows
// the normal permission mode.
type Risk int

const (
	RiskOrdinary Risk = iota
	// RiskUnknown is a command the classifier gave up on: wrappers nested
	// past MaxWrapperDepth. Nothing below the nest was graded, so it is
	// treated as needing the user's say-so rather than as ordinary — a
	// safety gate that answers "I could not tell" with "fine" is not a
	// gate, and `sh -c "sh -c \"sh -c ...\""` is not a shape an honest
	// command line takes.
	RiskUnknown
	// RiskDestructive writes or deletes through something the paths of
	// the command line do not show: an interpreter one-liner, xargs, or
	// a git command that discards work or rewrites history. It is a
	// warning on the prompt and never a safe read, but it does not force
	// a prompt: inside the workspace it follows the permission mode.
	RiskDestructive
	// RiskEscapes mutates paths outside the workspace root.
	RiskEscapes
	// RiskCatastrophic is never run, even with auto-approve.
	RiskCatastrophic
)

func (r Risk) String() string {
	switch r {
	case RiskUnknown:
		return "too deeply nested to classify"
	case RiskDestructive:
		return "destructive"
	case RiskEscapes:
		return "escapes workspace"
	case RiskCatastrophic:
		return "catastrophic"
	}
	return "ordinary"
}

// MaxWrapperDepth bounds unwrapping of sudo/env/sh -c/eval nests.
const MaxWrapperDepth = 4

// tooDeep is what every classifier returns when it hits the bound.
const tooDeep = "wrappers nested too deep: what this command would actually run cannot be worked out"

var catastrophic = []struct {
	re     *regexp.Regexp
	reason string
}{
	{regexp.MustCompile(`^rm\s+(?:-[a-zA-Z-]*\s+)*(?:/|/\*|~|~/|~/\*|\$HOME|\$\{HOME\}|/(?:home|etc|usr|var|boot|bin|sbin|lib|lib64|opt|root|dev|proc|sys)(?:/\*)?)(?:\s|$)`), "deletes the system or home directory"},
	{regexp.MustCompile(`^rm\s+(?:-[a-zA-Z-]*\s+)*-[a-zA-Z]*[rR][a-zA-Z]*\s+(?:/|/\*|~|~/|\$HOME|\$\{HOME\})(?:\s|$)`), "deletes the system or home directory"},
	{regexp.MustCompile(`^mkfs(?:\.\w+)?\b`), "formats a filesystem"},
	{regexp.MustCompile(`^dd\b.*\bof=/dev/`), "overwrites a raw device"},
	{regexp.MustCompile(`>\s*/dev/(?:sd|nvme|hd|vd|mmcblk|disk)`), "overwrites a raw device"},
	{regexp.MustCompile(`^(?:chmod|chown)\s+(?:-[a-zA-Z]*R[a-zA-Z]*|--recursive)\b.*\s/(?:\s|$)`), "changes ownership or permissions of the whole filesystem"},
	{regexp.MustCompile(`^(?:shutdown|reboot|halt|poweroff)\b|^init\s+[06]\b|^systemctl\s+(?:poweroff|reboot|halt)\b`), "powers the machine off"},
	{regexp.MustCompile(`^kill\s+-9\s+-1\b|^pkill\s+-9\s+\.\b`), "kills every process"},
	{regexp.MustCompile(`^(?:>|:>)\s*/etc/(?:passwd|shadow|fstab|sudoers)\b`), "destroys a system file"},
}

// wholeLine patterns span pipes, so they are checked before splitting.
var wholeLine = []struct {
	re     *regexp.Regexp
	reason string
}{
	// The shell may be named by its path (/bin/sh), behind sudo or env
	// with their options, and be any of the shells the classifier
	// unwraps: `curl x | /bin/sh` and `curl x | sudo -E bash` run the
	// download as surely as `curl x | sh`.
	{regexp.MustCompile(`\b(?:curl|wget)\b[^|]*\|\s*(?:(?:sudo|doas|env|command|exec)(?:\s+-\S+)*\s+)*(?:\S*/)?(?:ba|z|da|k)?sh\b`), "pipes a download into a shell"},
	{regexp.MustCompile(`:\(\)\s*\{\s*:\s*\|\s*:\s*&\s*\}\s*;\s*:`), "fork bomb"},
}

// mutators are commands whose path arguments are checked against the root.
// sed and perl are here for their -i (in-place edit) form: the file
// argument is the same kind of write target as tee's or cp's, and
// without them here neither Classify's workspace-escape check nor
// WriteTargets' locked-file check ever look at what such a command
// touches — exactly the gap a `sed -i ... locked_test.go` used to slip
// through (see accept_lock.go).
var mutators = map[string]bool{
	"rm": true, "rmdir": true, "mv": true, "cp": true, "chmod": true, "chown": true, "chgrp": true,
	"truncate": true, "tee": true, "ln": true, "install": true, "rsync": true, "shred": true, "mkdir": true, "touch": true,
	"sed": true, "perl": true, "unlink": true,
}

// mutatorTargets is the words of a mutator command that name what it
// writes. For most that is every non-flag argument. sed writes only in
// place (-i, --in-place, a cluster like -ni), and then only to its files,
// not to its script: `sed -n '/^type X/,/^}/p' f` had its address judged
// as a path outside the workspace, and a plain read was refused in
// headless runs. A script that can write or run something itself (w, W,
// e) keeps every word as a target. perl's script can do anything, so it
// keeps them too.
func mutatorTargets(toks []string) []string {
	words := argWords(toks)[1:]
	all := func() []string {
		var out []string
		for _, w := range words {
			if !strings.HasPrefix(w, "-") {
				out = append(out, w)
			}
			if toks[0] == "sed" {
				// The file a w command writes sits inside the script word.
				for _, m := range sedWriteFile.FindAllStringSubmatch(w, -1) {
					out = append(out, m[1])
				}
			}
		}
		return out
	}
	if toks[0] != "sed" {
		return all()
	}
	inPlace := false
	var scripts, operands []string
	for i := 0; i < len(words); i++ {
		w := words[i]
		switch {
		case w == "--":
			operands = append(operands, words[i+1:]...)
			i = len(words)
		case strings.HasPrefix(w, "--"):
			if strings.HasPrefix(w, "--in-place") {
				inPlace = true
			}
			if (w == "--expression" || w == "--file") && i+1 < len(words) {
				i++
				scripts = append(scripts, words[i])
			} else if v, ok := strings.CutPrefix(w, "--expression="); ok {
				scripts = append(scripts, v)
			} else if strings.HasPrefix(w, "--file=") {
				scripts = append(scripts, "")
			}
		case strings.HasPrefix(w, "-") && len(w) > 1:
			for j := 1; j < len(w); j++ {
				c := w[j]
				if c == 'i' {
					// -i takes an attached suffix and nothing after it.
					inPlace = true
					break
				}
				if c == 'e' || c == 'f' {
					v := w[j+1:]
					if v == "" && i+1 < len(words) {
						i++
						v = words[i]
					}
					if c == 'f' {
						v = "" // a script file: its contents are not judged
					}
					scripts = append(scripts, v)
					break
				}
			}
		default:
			operands = append(operands, w)
		}
	}
	if len(scripts) == 0 && len(operands) > 0 {
		scripts, operands = operands[:1], operands[1:]
	}
	if slices.ContainsFunc(scripts, sedScriptWrites) {
		return all()
	}
	if !inPlace {
		return nil
	}
	return operands
}

// sedWriteFile captures the path a sed w or W command writes.
var sedWriteFile = regexp.MustCompile(`[wW]\s+(\S+)`)

// sedScriptWrites reports whether a sed script can write a file or run
// a command itself: the w, W and e commands, the s///w and s///e flags,
// and, since its flags cannot be read reliably, any s command with a
// delimiter other than /.
func sedScriptWrites(script string) bool {
	return sedWriteCmd.MatchString(script) || sedSubstWrites.MatchString(script) || sedOddSubst.MatchString(script)
}

var (
	sedWriteCmd    = regexp.MustCompile(`(^|[;{}/\s\d$])[wWe](\s|$)`)
	sedSubstWrites = regexp.MustCompile(`(^|[;{}\s\d$/])s/(?:[^/\\]|\\.)*/(?:[^/\\]|\\.)*/[gpiImM0-9]*[we]`)
	sedOddSubst    = regexp.MustCompile(`(^|[;{}\s\d$])s[^/\w\s\\]`)
)

// outputFlags are the flags whose argument is a path the command writes,
// per command. Only flags that certainly take a path are listed: curl's
// -O takes none and wget's -O does, so guessing from the letter would
// judge the URL after `curl -O https://…` as a file.
var outputFlags = map[string][]string{
	"go":      {"-o"},
	"cc":      {"-o"},
	"gcc":     {"-o"},
	"g++":     {"-o"},
	"clang":   {"-o"},
	"clang++": {"-o"},
	"c++":     {"-o"},
	"rustc":   {"-o"},
	"ld":      {"-o"},
	"objcopy": {"-o"},
	"curl":    {"-o", "--output"},
	"wget":    {"-O", "--output-document"},
	"sort":    {"-o"},
	"gzip":    {"-o"},
	"ffmpeg":  {"-o"},
	"dd":      {"of="},
}

// outputArg reads the path an output flag names, in either spelling the
// flag takes: separated (`-o path`, and then i moves past it) or
// attached (`-opath`, `--output=path`).
func outputArg(toks []string, flags []string, i *int) (string, bool) {
	tk := toks[*i]
	for _, f := range flags {
		switch {
		case tk == f:
			if *i+1 >= len(toks) {
				return "", false
			}
			*i++
			return toks[*i], true
		case strings.HasPrefix(f, "--"):
			if v, ok := strings.CutPrefix(tk, f+"="); ok {
				return v, true
			}
		case len(tk) > len(f) && strings.HasPrefix(tk, f):
			return tk[len(f):], true
		}
	}
	return "", false
}

// Classify grades a command line run with root as the working directory.
// The reason describes the first problem found.
func Classify(command, root string) (Risk, string) {
	return classify(WindowsPaths(command), root, 0)
}

// ClassifyFrom is Classify for a shell already standing in cwd, which is
// not root: a persistent terminal that an earlier send cd'd. known false
// means the directory could not be followed, and relative paths are then
// judged as they are after an unresolvable cd.
func ClassifyFrom(command, root, cwd string, known bool) (Risk, string) {
	return classifyAt(WindowsPaths(command), root, cwd, known, 0)
}

// EndCwd is where a shell that stood in cwd is standing after running
// command: the effect of its cd, pushd and popd segments. A target that
// cannot be resolved statically (a variable, a substitution, `cd -`,
// popd) leaves the directory unknown.
func EndCwd(command, cwd string, known bool) (string, bool) {
	command = ifsRe.ReplaceAllString(WindowsPaths(command), " ")
	for _, seg := range Segments(command) {
		if dir, ok, isCd := cdTokens(Words(seg), cwd, known); isCd {
			cwd, known = dir, ok
		}
	}
	return cwd, known
}

// classify is Classify with the wrapper depth carried in. A `sh -c` or an
// `eval` body is another command line, and it is classified at the depth
// its wrappers have already reached: starting it again at zero would let
// `eval eval eval …` unwrap for as long as the model cares to nest, which
// is the bound existing to be worked around.
func classify(command, root string, depth int) (Risk, string) {
	return classifyAt(command, root, root, root != "", depth)
}

// classifyAt is classify with the effective working directory carried in.
// `cd` segments change where the later segments' relative paths land, so
// each segment is graded against the directory the shell would actually
// be standing in — `cd ~ && rm -rf wspace` deletes ~/wspace, not
// <root>/wspace. A `cd` to something unresolvable (a variable, a glob,
// `cd -`) makes the directory unknown, and mutators after it can no
// longer be judged: they are treated as needing the user's say-so.
func classifyAt(command, root, cwd string, cwdKnown bool, depth int) (Risk, string) {
	if depth > MaxWrapperDepth {
		return RiskUnknown, tooDeep
	}
	command = ifsRe.ReplaceAllString(command, " ")
	unquoted := stripQuoted(command)
	for _, c := range wholeLine {
		if c.re.MatchString(unquoted) {
			return RiskCatastrophic, c.reason
		}
	}
	worst, reason := RiskOrdinary, ""
	note := func(r Risk, why string) {
		if r > worst {
			worst, reason = r, why
		}
	}
	entryCwd, entryKnown, moved := cwd, cwdKnown, false
	inCase := false
	for _, seg := range Segments(command) {
		toks := Words(seg)
		if dir, known, isCd := cdTokens(toks, cwd, cwdKnown); isCd {
			cwd, cwdKnown, moved = dir, known, true
			continue
		}
		note(classifyOne(seg, toks, root, cwd, cwdKnown, depth))
		var arm []string
		if arm, inCase = caseArm(toks, inCase); len(arm) > 0 {
			note(classifyOne(seg, arm, root, cwd, cwdKnown, depth))
		}
	}
	// A substitution is a command line of its own, run before the
	// command that holds it — `echo $(rm -rf /)` runs the rm. Where on
	// the line it sits is not tracked, so after a cd it is also judged
	// from an unknown directory.
	for _, body := range substitutions(command) {
		note(classifyAt(body, root, entryCwd, entryKnown, depth+1))
		if moved {
			note(classifyAt(body, root, "", false, depth+1))
		}
	}
	return worst, reason
}

// caseArm reads a segment of a case statement. Segments has already
// split the arms apart at their ;; and blanked the ) that ends each
// pattern, so `case $x in a) rm -rf ~/y;; esac` arrives as a segment
// whose words are case, $x, in, a, rm, … and the command an arm runs
// sits behind its pattern word, where nothing judged it. arm is the
// segment's words with the pattern (and the case head) dropped — to be
// judged as well as the words as they stand, since a pattern spelled
// with spaces around a | leaves the pattern guess one word out — and
// stillIn whether the segments after this one are still arms.
func caseArm(toks []string, inCase bool) (arm []string, stillIn bool) {
	w := commandWords(toks)
	if len(w) > 0 && w[0] == "case" {
		k := slices.Index(w, "in")
		if k < 0 || k+2 > len(w) {
			return nil, true
		}
		return w[k+2:], true
	}
	if !inCase {
		return nil, false
	}
	if len(w) > 0 && w[0] == "esac" {
		return nil, false
	}
	if len(w) > 1 {
		return w[1:], true
	}
	return nil, true
}

// substitutions returns the bodies of the command and process
// substitutions in s — $(…), `…`, <(…) and >(…) — outside single quotes.
// Double quotes do not stop them: `echo "$(rm -rf x)"` runs the rm. The
// bodies are not searched here for substitutions of their own; judging a
// body as a command line does that.
func substitutions(s string) []string {
	var out []string
	for _, sp := range substitutionSpans(s) {
		out = append(out, s[sp.body[0]:sp.body[1]])
	}
	return out
}

// SplitSubstitutions separates a command line from the command lines it
// substitutes: outer is s with each outermost $(…), `…`, <(…) and >(…)
// replaced by the word _, and bodies are what they held, in order. A
// body can hold substitutions of its own; splitting it again finds them.
// It is how a permission rule is kept to the command it names — an
// allow rule's * spans any text, and `go test $(curl …)` runs curl.
func SplitSubstitutions(s string) (outer string, bodies []string) {
	spans := substitutionSpans(s)
	if len(spans) == 0 {
		return s, nil
	}
	var b strings.Builder
	last := 0
	for _, sp := range spans {
		b.WriteString(s[last:sp.whole[0]])
		b.WriteString("_")
		last = sp.whole[1]
		bodies = append(bodies, s[sp.body[0]:sp.body[1]])
	}
	b.WriteString(s[last:])
	return b.String(), bodies
}

// substSpan is where one substitution sits in a line: whole is the text
// from its opening $, `, < or > to just past its closing ) or ` (or the
// end of the line when it is never closed), and body is what it runs.
type substSpan struct{ whole, body [2]int }

// substitutionSpans finds the outermost substitutions in s; see
// substitutions for what counts as one.
func substitutionSpans(s string) []substSpan {
	var out []substSpan
	quote := byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote == '\'':
			if c == '\'' {
				quote = 0
			}
			continue
		case quote == '$':
			if c == '\\' {
				i++
			} else if c == '\'' {
				quote = 0
			}
			continue
		case c == '\\':
			i++
			continue
		case c == '"':
			if quote == '"' {
				quote = 0
			} else {
				quote = '"'
			}
			continue
		case quote == 0 && c == '$' && i+1 < len(s) && s[i+1] == '\'':
			quote = '$'
			i++
			continue
		case quote == 0 && c == '\'':
			quote = '\''
			continue
		}
		switch {
		case c == '`':
			end := i + 1
			for end < len(s) && s[end] != '`' {
				if s[end] == '\\' {
					end++
				}
				end++
			}
			end = min(end, len(s))
			out = append(out, substSpan{whole: [2]int{i, min(end+1, len(s))}, body: [2]int{i + 1, end}})
			i = end
		case c == '$' && strings.HasPrefix(s[i+1:], "(("):
			// Arithmetic. Its text can still hold a substitution, which
			// the scan finds as it carries on.
			i += 2
		case (c == '$' || (quote == 0 && (c == '<' || c == '>'))) && i+1 < len(s) && s[i+1] == '(':
			end := closeParen(s, i+2)
			out = append(out, substSpan{whole: [2]int{i, min(end+1, len(s))}, body: [2]int{i + 2, end}})
			i = end
		}
	}
	return out
}

// closeParen returns the index of the parenthesis that closes one opened
// just before s[j], skipping quoted text, or len(s) when there is none —
// an unclosed body runs to the end of the line, which is the most it can
// hold.
func closeParen(s string, j int) int {
	depth := 1
	quote := byte(0)
	for ; j < len(s); j++ {
		c := s[j]
		switch {
		case quote == '\'':
			if c == '\'' {
				quote = 0
			}
		case c == '\\':
			j++
		case quote == '"':
			if c == '"' {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '(':
			depth++
		case c == ')':
			depth--
			if depth == 0 {
				return j
			}
		}
	}
	return len(s)
}

// cdTokens parses a segment that is nothing but `cd <dir>` (Segments has
// already split at &&/; so no other command can share the segment) and
// resolves the new working directory. isCd is false for anything else.
// A target that cannot be resolved leaves the directory known=false; a
// target is also unresolvable when the current directory is already
// unknown and the target is relative.
func cdTokens(toks []string, cwd string, cwdKnown bool) (dir string, known, isCd bool) {
	// `{ cd /; …` and `if cd /; then …` move the shell as plainly as a
	// bare cd, and pushd moves it as well; popd goes back to a directory
	// this parser never tracked.
	toks = commandWords(toks)
	if len(toks) == 0 {
		return "", false, false
	}
	switch toks[0] {
	case "cd":
	case "pushd":
		if len(toks) != 2 || strings.HasPrefix(toks[1], "+") || strings.HasPrefix(toks[1], "-") {
			// pushd alone swaps the top two directories and pushd +N
			// rotates the stack: either way, to somewhere unknown.
			return "", false, true
		}
	case "popd":
		return "", false, true
	default:
		return "", false, false
	}
	if len(toks) > 2 {
		// A cd this parser does not understand is still a cd: `cd -- /`,
		// `cd -P /`, `cd / 2>/dev/null`. Reporting isCd=false handed the
		// segment to classifyOne, which shrugs at an unknown command, and
		// the tracked directory stayed at the workspace root — so
		// `cd -- / && rm -rf home` was judged as deleting ./home and
		// graded ordinary. Not knowing where the shell stands is the
		// honest answer, and it makes the rest of the line unjudgeable.
		return "", false, true
	}
	if len(toks) == 1 {
		if home, err := os.UserHomeDir(); err == nil {
			return home, true, true
		}
		return "", false, true
	}
	tk := toks[1]
	if tk == "-" {
		return "", false, true
	}
	if !cwdKnown && !rooted(tk) && !strings.HasPrefix(tk, "~") && !strings.HasPrefix(tk, "$HOME") {
		return "", false, true
	}
	abs, ok := resolvePathArg(tk, cwd)
	if !ok {
		return "", false, true
	}
	return abs, true, true
}

func classifyOne(seg string, toks []string, root string, cwd string, cwdKnown bool, depth int) (Risk, string) {
	seg = strings.TrimSpace(seg)
	if seg == "" || len(toks) == 0 {
		return RiskOrdinary, ""
	}
	if depth > MaxWrapperDepth {
		return RiskUnknown, tooDeep
	}
	return classifyWords(toks, seg, root, cwd, cwdKnown, depth)
}

// classifyWords peels wrappers so `sudo sh -c "rm -rf /"` is judged by what
// it runs. seg is the raw text, for redirection checks.
func classifyWords(toks []string, seg, root string, cwd string, cwdKnown bool, depth int) (Risk, string) {
	r, why := classifyPaths(toks, seg, root, cwd, cwdKnown, depth)
	if r >= RiskDestructive {
		return r, why
	}
	r2, why2 := destructiveRisk(commandWords(toks))
	return worse(r, why, r2, why2)
}

// classifyPaths grades a command by the paths it names; destructiveRisk
// covers what it does to them without naming any.
func classifyPaths(toks []string, seg, root string, cwd string, cwdKnown bool, depth int) (Risk, string) {
	if len(toks) == 0 {
		return RiskOrdinary, ""
	}
	if depth > MaxWrapperDepth {
		return RiskUnknown, tooDeep
	}
	toks = commandWords(toks)
	if len(toks) == 0 {
		return redirectRisk(seg, root, cwd, cwdKnown)
	}
	switch toks[0] {
	case "sudo", "doas", "nice", "nohup", "time", "command", "exec", "builtin", "stdbuf", "unbuffer", "timeout", "xargs",
		"setsid", "ionice", "taskset", "chrt", "busybox":
		return classifyWords(wrapped(toks), seg, root, cwd, cwdKnown, depth+1)
	case "env":
		inner, chdir, _ := envWrapped(toks)
		if chdir != "" {
			if !cwdKnown && !isAbsoluteWord(chdir) {
				cwd, cwdKnown = "", false
			} else if dir, ok := resolvePathArg(chdir, cwd); ok {
				cwd = dir
			} else {
				cwd, cwdKnown = "", false
			}
		}
		return classifyWords(inner, seg, root, cwd, cwdKnown, depth+1)
	case "sh", "bash", "zsh", "dash", "ksh":
		if line, ok := shellCommand(toks); ok {
			// The wrapped command line starts in the same directory
			// the wrapper segment runs in. The wrapper's own
			// redirection (`bash -c ls > ~/.bashrc`) writes too.
			r, why := classifyAt(line, root, cwd, cwdKnown, depth+1)
			r2, why2 := redirectRisk(seg, root, cwd, cwdKnown)
			return worse(r, why, r2, why2)
		}
	case "eval":
		r, why := classifyAt(strings.Join(toks[1:], " "), root, cwd, cwdKnown, depth+1)
		r2, why2 := redirectRisk(seg, root, cwd, cwdKnown)
		return worse(r, why, r2, why2)
	}
	// Judged as written, for the patterns that read a redirection, and
	// with redirections cut off the words, for those that read paths.
	line, args := strings.Join(toks, " "), strings.Join(argWords(toks), " ")
	for _, c := range catastrophic {
		if c.re.MatchString(line) || c.re.MatchString(args) {
			return RiskCatastrophic, c.reason
		}
	}
	if toks[0] == "find" {
		if r, why := findRisk(toks, seg, root, cwd, cwdKnown, depth); r != RiskOrdinary {
			return r, why
		}
	}
	if mutators[toks[0]] {
		for _, tk := range mutatorTargets(toks) {
			if root != "" && !cwdKnown && !isAbsoluteWord(tk) {
				// The directory is unknown after a cd the classifier
				// could not resolve; a relative word resolves
				// somewhere in it, which cannot be judged.
				return RiskUnknown, "the working directory is unclear after cd, so " + toks[0] + " " + tk + " cannot be judged"
			}
			abs, ok := resolvePathArg(tk, cwd)
			if !ok {
				if r, why := fuzzyTarget(toks[0], tk, cwd, cwdKnown, root); r != RiskOrdinary {
					return r, why
				}
				continue
			}
			if catastrophicTarget(abs) {
				return RiskCatastrophic, toks[0] + " targets " + abs
			}
			if p, out := escapesResolved(abs, tk, root); out {
				return RiskEscapes, toks[0] + " touches " + p + ", outside the workspace"
			}
		}
	}
	// An output flag names a write target as plainly as cp's second
	// argument or a `>` does, and until this read it, `go build -o
	// ~/bin/svc-lint` was graded ordinary — which is how two recorded
	// eval runs installed a binary into the user's home without the
	// guard ever looking. The flag lists are per command because -o is
	// an output path for cc and a remote-name switch for curl.
	if flags, ok := outputFlags[toks[0]]; ok {
		for i := 1; i < len(toks); i++ {
			tk, isOut := outputArg(toks, flags, &i)
			if !isOut || tk == "" {
				continue
			}
			if root != "" && !cwdKnown && !isAbsoluteWord(tk) {
				return RiskUnknown, "the working directory is unclear after cd, so writing " + tk + " cannot be judged"
			}
			abs, ok := resolvePathArg(tk, cwd)
			if !ok {
				if r, why := fuzzyTarget("write", tk, cwd, cwdKnown, root); r != RiskOrdinary {
					return r, why
				}
				continue
			}
			if catastrophicTarget(abs) {
				return RiskCatastrophic, toks[0] + " writes to " + abs
			}
			if p, out := escapesResolved(abs, tk, root); out {
				return RiskEscapes, toks[0] + " writes to " + p + ", outside the workspace"
			}
		}
	}
	return redirectRisk(seg, root, cwd, cwdKnown)
}

// findCommand splits a find invocation into its starting points and the
// actions that change things: whether it -deletes, the files its
// -fprint family writes, and the commands its -exec family runs (with {}
// standing for the files found, left in place).
func findCommand(toks []string) (starts []string, deletes bool, writes []string, execs [][]string) {
	i := 1
	for i < len(toks) && (toks[i] == "-H" || toks[i] == "-L" || toks[i] == "-P" || strings.HasPrefix(toks[i], "-O") || toks[i] == "-D") {
		if toks[i] == "-D" {
			i++
		}
		i++
	}
	for ; i < len(toks); i++ {
		tk := toks[i]
		if strings.HasPrefix(tk, "-") || tk == "(" || tk == "!" || tk == ")" {
			break
		}
		starts = append(starts, tk)
	}
	if len(starts) == 0 {
		starts = []string{"."}
	}
	for ; i < len(toks); i++ {
		switch toks[i] {
		case "-delete":
			deletes = true
		case "-fprint", "-fprint0", "-fprintf", "-fls":
			if i+1 < len(toks) {
				writes = append(writes, toks[i+1])
				i++
			}
		case "-exec", "-execdir", "-ok", "-okdir":
			j := i + 1
			for j < len(toks) && toks[j] != ";" && toks[j] != "+" {
				j++
			}
			if j > i+1 {
				execs = append(execs, toks[i+1:j])
			}
			i = j
		}
	}
	return starts, deletes, writes, execs
}

// findRisk grades what a find changes. `find / -delete`, `find ~ -exec rm
// {} +` and `find . -exec rm -rf / \;` were all ordinary: find is not a
// mutator, so nothing looked at its actions. The command an -exec runs is
// judged like any other, with {} as a file of the current directory; and
// a find that deletes, or hands its files to a mutator, changes what lies
// under its starting points, so those are judged as write targets.
// Outside the workspace that asks rather than refuses: the filters decide
// what is touched, and `find ~/x -name '*.orig' -delete` is not rm -rf.
func findRisk(toks []string, seg, root, cwd string, cwdKnown bool, depth int) (Risk, string) {
	starts, deletes, writes, execs := findCommand(toks)
	worst, reason := RiskOrdinary, ""
	for _, w := range writes {
		r, why := judgeWrite("find writes", w, root, cwd, cwdKnown)
		worst, reason = worse(worst, reason, r, why)
	}
	for _, ex := range execs {
		sub := make([]string, len(ex))
		for k, w := range ex {
			sub[k] = strings.ReplaceAll(w, "{}", ".")
		}
		r, why := classifyWords(sub, strings.Join(sub, " "), root, cwd, cwdKnown, depth+1)
		worst, reason = worse(worst, reason, r, why)
		inner := commandWords(ex)
		for n := 0; n <= MaxWrapperDepth && len(inner) > 0; n++ {
			if inner[0] == "env" {
				inner, _, _ = envWrapped(inner)
			} else if _, ok := wrapperArgFlags[inner[0]]; ok || inner[0] == "nohup" || inner[0] == "command" || inner[0] == "builtin" || inner[0] == "unbuffer" {
				inner = wrapped(inner)
			} else {
				break
			}
			inner = commandWords(inner)
		}
		if len(inner) > 0 && mutators[inner[0]] && slices.ContainsFunc(ex, func(w string) bool { return strings.Contains(w, "{}") }) {
			deletes = true
		}
	}
	if deletes {
		for _, s := range starts {
			r, why := judgeWrite("find changes files under", s, root, cwd, cwdKnown)
			if r == RiskCatastrophic {
				r = RiskEscapes
			}
			worst, reason = worse(worst, reason, r, why)
		}
	}
	return worst, reason
}

// judgeWrite grades one path a command writes, the way the redirect and
// output-flag checks do.
func judgeWrite(verb, tk, root, cwd string, cwdKnown bool) (Risk, string) {
	if root != "" && !cwdKnown && !isAbsoluteWord(tk) {
		return RiskUnknown, "the working directory is unclear after cd, so " + verb + " " + tk + " cannot be judged"
	}
	abs, ok := resolvePathArg(tk, cwd)
	if !ok {
		return fuzzyTarget(verb, tk, cwd, cwdKnown, root)
	}
	if catastrophicTarget(abs) {
		return RiskCatastrophic, verb + " " + abs
	}
	if p, out := escapesResolved(abs, tk, root); out {
		return RiskEscapes, verb + " " + p + ", outside the workspace"
	}
	return RiskOrdinary, ""
}

// redirectRisk grades a segment's redirections: they write wherever they
// point.
func redirectRisk(seg, root, cwd string, cwdKnown bool) (Risk, string) {
	for _, raw := range redirectTargets(seg) {
		tk := strings.Trim(raw, `"'`)
		if root != "" && !cwdKnown && !isAbsoluteWord(tk) {
			return RiskUnknown, "the working directory is unclear after cd, so writing " + tk + " cannot be judged"
		}
		abs, ok := resolvePathArg(tk, cwd)
		if !ok {
			if r, why := fuzzyTarget("write", tk, cwd, cwdKnown, root); r != RiskOrdinary {
				return r, why
			}
			continue
		}
		if catastrophicTarget(abs) {
			return RiskCatastrophic, "writes to " + abs
		}
		if p, out := escapesResolved(abs, tk, root); out {
			return RiskEscapes, "writes to " + p + ", outside the workspace"
		}
	}
	return RiskOrdinary, ""
}

// worse returns the graver of two verdicts, the first on a tie.
func worse(r1 Risk, why1 string, r2 Risk, why2 string) (Risk, string) {
	if r2 > r1 {
		return r2, why2
	}
	return r1, why1
}

// argWords is a command's words with any redirection written onto one
// cut off: Words does not split at > or <, so `rm -rf />/dev/null` came
// through as rm of a path named "/>/dev/null" — outside the workspace,
// but not the root it deletes, and refused only as far as asking. A word
// that is all redirection is dropped. The redirection itself is judged
// from the raw segment (redirectRisk), so nothing is lost. Only for
// path arguments and the catastrophic patterns: a sh -c body is a quoted
// word that may well hold a >, and cutting it would hide its tail.
func argWords(toks []string) []string {
	out := make([]string, 0, len(toks))
	for i, tk := range toks {
		if i > 0 {
			if k := strings.IndexAny(tk, "<>"); k >= 0 {
				tk = strings.TrimSuffix(tk[:k], "&")
			}
			if tk == "" {
				continue
			}
		}
		out = append(out, tk)
	}
	return out
}

// ifsRe matches an unexpanded $IFS, which the shell splits into the
// space it holds: `rm${IFS}-rf${IFS}/` is `rm -rf /` spelled as one word.
// An IFS the line sets itself is not followed; this is the spelling that
// needs no setup.
var ifsRe = regexp.MustCompile(`\$(?:\{IFS\}|IFS\b)`)

// assignmentRe matches a VAR=value word.
var assignmentRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*\+?=`)

// leadingKeywords are the reserved words a command can follow in the
// same segment once Segments has split at ; && || | and newlines. The
// closing words (}, fi, done, esac) are left alone: one may carry the
// redirection of the whole compound command, `{ ls; } > ~/x`, and a
// segment that is only an unknown word is still checked for that.
var leadingKeywords = map[string]bool{
	"{": true, "!": true, "if": true, "then": true, "else": true, "elif": true,
	"do": true, "while": true, "until": true,
}

// commandWords returns a simple command's words from the command name
// on: the VAR=value assignments before it are dropped, and a name given
// as a path is reduced to the program it names. `X=1 rm -rf /` and
// `/bin/rm -rf /` both run rm, and were both graded as some command the
// classifier had never heard of. The reserved words that open a compound
// command (`{`, `!`, if, then, do, …) and a function definition's head
// are dropped too: `{ rm -rf /; }` and `if true; then rm -rf ~/x; fi` run
// rm, and were graded as commands named { and then.
func commandWords(toks []string) []string {
lead:
	for len(toks) > 0 {
		switch {
		case assignmentRe.MatchString(toks[0]), leadingKeywords[toks[0]]:
			toks = toks[1:]
		case toks[0] == "function" && len(toks) > 1:
			toks = toks[2:] // function NAME { …
		case len(toks) > 1 && toks[1] == "{":
			toks = toks[2:] // NAME() { …, the parentheses already gone
		default:
			break lead
		}
	}
	if len(toks) > 0 && strings.Contains(toks[0], "/") && toks[0] != "/" {
		toks = append([]string{path.Base(toks[0])}, toks[1:]...)
	}
	return toks
}

// wrapperArgFlags are, per wrapper, the options that take the next word
// as their argument. Skipping every leading dash-word and nothing else
// made the argument the command: `sudo -u root rm -rf /` was judged as a
// command named "root", `timeout -s KILL 5 rm -rf /` as one named "5".
var wrapperArgFlags = map[string][]string{
	"sudo":    {"-u", "-g", "-C", "-D", "-p", "-r", "-t", "-U", "-T", "--user", "--group", "--close-from", "--chdir", "--prompt", "--role", "--type", "--other-user", "--command-timeout"},
	"doas":    {"-u", "-C"},
	"nice":    {"-n", "--adjustment"},
	"exec":    {"-a"},
	"time":    {"-f", "-o", "--format", "--output"},
	"stdbuf":  {"-i", "-o", "-e", "--input", "--output", "--error"},
	"timeout": {"-s", "-k", "--signal", "--kill-after"},
	"xargs":   {"-a", "-d", "-E", "-I", "-L", "-n", "-P", "-s", "--arg-file", "--delimiter", "--eof", "--replace", "--max-lines", "--max-args", "--max-procs", "--max-chars", "--process-slot-var"},
	// setsid, ionice, taskset, chrt and busybox run the command after
	// them as surely as nice does; `setsid rm -rf ~/x` was graded as
	// a command named setsid and nothing more.
	"setsid":  {},
	"ionice":  {"-c", "-n", "--class", "--classdata"},
	"taskset": {},
	"chrt":    {"-T", "-P", "-D", "--sched-runtime", "--sched-period", "--sched-deadline"},
	"busybox": {},
}

// wrapped returns the command a wrapper (sudo, nice, timeout, xargs, …)
// runs: its words after the wrapper's options, their arguments and, for
// timeout, the duration.
func wrapped(toks []string) []string {
	flags := wrapperArgFlags[toks[0]]
	i := 1
	for i < len(toks) && strings.HasPrefix(toks[i], "-") && toks[i] != "-" {
		if toks[i] == "--" {
			i++
			break
		}
		if slices.Contains(flags, toks[i]) {
			i++
		}
		i++
	}
	switch toks[0] {
	case "timeout":
		i++ // the duration
	case "taskset", "chrt":
		i++ // the CPU mask, the priority
	}
	if i >= len(toks) {
		return nil
	}
	return toks[i:]
}

// envWrapped returns the command an env invocation runs, the directory
// its -C moves it to ("" for none), and whether it sets or unsets
// anything in the environment. -u names a variable rather than the
// command, and -S splits its argument into more of env's own words,
// which is to say it can carry the whole command.
func envWrapped(toks []string) (inner []string, chdir string, changes bool) {
	i := 1
	for i < len(toks) {
		tk := toks[i]
		next := func() string {
			if i+1 < len(toks) {
				i++
				return toks[i]
			}
			return ""
		}
		switch {
		case tk == "--":
			i++
			return toks[min(i, len(toks)):], chdir, changes
		case assignmentRe.MatchString(tk):
			changes = true
		case tk == "-u" || tk == "--unset":
			next()
			changes = true
		case strings.HasPrefix(tk, "--unset="):
			changes = true
		case tk == "-C" || tk == "--chdir":
			chdir = next()
		case strings.HasPrefix(tk, "--chdir="):
			chdir = strings.TrimPrefix(tk, "--chdir=")
		case tk == "-S" || tk == "--split-string":
			s := next()
			rest, d, c := envWrapped(append(append([]string{"env"}, Words(s)...), toks[i+1:]...))
			return rest, cmp.Or(d, chdir), c || changes
		case strings.HasPrefix(tk, "--split-string="):
			rest, d, c := envWrapped(append(append([]string{"env"}, Words(strings.TrimPrefix(tk, "--split-string="))...), toks[i+1:]...))
			return rest, cmp.Or(d, chdir), c || changes
		case len(tk) > 1 && tk[0] == '-' && tk[1] != '-':
			// A cluster of short options; u, C and S take the rest of
			// the cluster, or the next word, as their argument.
			k := strings.IndexAny(tk, "uCS")
			if k < 0 {
				break
			}
			arg := tk[k+1:]
			if arg == "" {
				arg = next()
			}
			switch tk[k] {
			case 'u':
				changes = true
			case 'C':
				chdir = arg
			case 'S':
				rest, d, c := envWrapped(append(append([]string{"env"}, Words(arg)...), toks[i+1:]...))
				return rest, cmp.Or(d, chdir), c || changes
			}
		case strings.HasPrefix(tk, "-"):
		default:
			return toks[i:], chdir, changes
		}
		i++
	}
	return nil, chdir, changes
}

// shellCommand returns the command line a `sh -c` invocation runs: the
// first word after the shell's options, when one of the short-option
// clusters among them holds c. Looking for a "c" in any dash-word and
// taking the word after it read `bash --norc -c 'rm -rf /'` as running
// "-c", and `bash -c -e 'rm -rf /'` as running "-e".
func shellCommand(toks []string) (string, bool) {
	hasC := false
	i := 1
	for ; i < len(toks); i++ {
		tk := toks[i]
		if tk == "--" || tk == "-" {
			i++
			break
		}
		if len(tk) < 2 || (tk[0] != '-' && tk[0] != '+') {
			break
		}
		if strings.HasPrefix(tk, "--") {
			if tk == "--rcfile" || tk == "--init-file" {
				i++
			}
			continue
		}
		if tk[0] == '-' && strings.Contains(tk, "c") {
			hasC = true
		}
		if strings.ContainsAny(tk[1:], "oO") {
			i++ // -o/+o/-O name an option in the next word
		}
	}
	if !hasC || i >= len(toks) {
		return "", false
	}
	return toks[i], true
}

// isAbsoluteWord reports whether a path token names a fixed location
// whatever the working directory is: an absolute path, one anchored at
// ~, or one whose variables expand to an absolute prefix ($HOME/x, ${TMPDIR}).
func isAbsoluteWord(tk string) bool {
	if rooted(tk) || strings.HasPrefix(tk, "~") {
		return true
	}
	return rooted(shellVarRe.ReplaceAllString(tk, "/"))
}

// rooted reports whether a path names a fixed place rather than one
// relative to the working directory. On Windows that includes a path
// that starts with a slash: filepath.IsAbs says no to /etc/hosts there,
// yet PowerShell and every Windows program read it as \etc\hosts on the
// current drive, which is no more inside the workspace than C:\etc\hosts.
// The classifier took such paths for relative ones, so `echo x >
// /etc/hosts` and `rm -rf /Users/me` were ordinary commands on Windows.
func rooted(p string) bool {
	if filepath.IsAbs(p) {
		return true
	}
	return runtime.GOOS == "windows" && (strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`))
}

// WindowsPaths readies a command line for the POSIX word splitting the
// classifier does. The shell on Windows is PowerShell, where a backslash
// is an ordinary character (its escape is the backtick) and is how every
// path is written; read as a POSIX escape it vanished, so C:\Users became
// C:Users and \Windows became Windows, a relative name, and neither was
// judged. A forward slash means the same to Windows and to filepath, so a
// backslash that separates path elements — one followed by a letter,
// digit, dot, underscore or dash — becomes a slash before the words are
// split. One before a quote, a dollar sign, a newline or another
// backslash is left as the escape a bash-minded command means by it:
// models write bash quoting on Windows too, and reading it as bash keeps
// the quoting checks' judgement.
func WindowsPaths(command string) string {
	if runtime.GOOS != "windows" || !strings.Contains(command, `\`) {
		return command
	}
	b := []byte(command)
	for i := 0; i+1 < len(b); i++ {
		if b[i] != '\\' {
			continue
		}
		c := b[i+1]
		if c == '\\' {
			i++ // an escaped backslash stays one
			continue
		}
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-' {
			b[i] = '/'
		}
	}
	return string(b)
}

// absFrom resolves p from cwd as the operating system would: a rooted
// path as itself (on Windows, a slash-rooted one on cwd's drive), any
// other joined to cwd.
func absFrom(p, cwd string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	if rooted(p) {
		return filepath.Clean(filepath.VolumeName(cwd) + p)
	}
	return filepath.Clean(filepath.Join(cwd, p))
}

// redirect matches an output redirection: the operator (group 1) and
// its target as written (group 2). The operator takes every form bash
// writes to a file with — >, >>, a numbered stream (2>, 1>>), the
// clobbering >|, and &>, &>>, >& that send both streams. The pattern used
// to refuse a digit before the >, which made `ls 2>~/.bashrc` no write
// at all.
var redirect = regexp.MustCompile(`(?:^|[^<>])(&>>?|[0-9]*>>?[|&]?)\s*("[^"]+"|'[^']+'|[^\s;&|<>]+)`)

// redirectTargets returns the targets of a segment's output redirections,
// as written. A >& whose target is a stream number or - duplicates or
// closes a stream (2>&1, >&2) and writes no file.
func redirectTargets(seg string) []string {
	var out []string
	for _, m := range redirect.FindAllStringSubmatch(seg, -1) {
		if strings.HasSuffix(m[1], "&") && strings.Trim(m[2], "0123456789-") == "" {
			continue
		}
		out = append(out, m[2])
	}
	return out
}

// escapesResolved reports whether a resolved absolute path lands outside
// root. tk is the token it came from, kept so the temp-dir exemption can
// require an explicit path: a token that reaches temp only through `..`
// (from a workspace that lives under it) is judged, not exempted.
func escapesResolved(abs, tk, root string) (string, bool) {
	if root == "" {
		return "", false
	}
	rootClean := filepath.Clean(root)
	if abs == rootClean || strings.HasPrefix(abs, rootClean+string(filepath.Separator)) {
		return "", false
	}
	if abs == "/dev/null" || strings.HasPrefix(abs, "/dev/std") {
		return "", false
	}
	// The null device by its Windows names, and by the POSIX one a model
	// writes out of habit (which there resolves to \dev\null and fails
	// rather than writing anywhere that matters).
	if runtime.GOOS == "windows" && nullDevice(tk) {
		return "", false
	}
	// Scratch files under the temp dir are fine; the resolved path must
	// actually land there — /tmp/../home/... cleans outside and is judged.
	if underTemp(abs) && directPath(tk) {
		return "", false
	}
	return abs, true
}

// underTemp reports whether abs is the process temp dir or inside it.
func underTemp(abs string) bool {
	tmp := filepath.Clean(os.TempDir())
	return abs == tmp || strings.HasPrefix(abs, tmp+string(filepath.Separator))
}

// directPath reports whether the token reaches its cleaned path without
// a .. element: explicit paths only.
func directPath(tk string) bool {
	if rooted(tk) {
		return true
	}
	for _, el := range strings.Split(filepath.ToSlash(tk), "/") {
		if el == ".." {
			return false
		}
	}
	return true
}

// systemDirs are the top-level directories the ^rm catastrophic patterns
// guard; catastrophicTarget matches them on resolved paths so that
// `cd / && rm -rf home` is graded like `rm -rf /home`.
var systemDirs = []string{"/home", "/etc", "/usr", "/var", "/boot", "/bin", "/sbin", "/lib", "/lib64", "/opt", "/root", "/dev", "/proc", "/sys"}

// windowsSystemDirs are their counterparts on a Windows drive.
var windowsSystemDirs = []string{`\Windows`, `\Users`, `\Program Files`, `\Program Files (x86)`, `\ProgramData`}

func catastrophicTarget(abs string) bool {
	if abs == "/" {
		return true
	}
	if runtime.GOOS == "windows" {
		// A drive's root and the directories that hold the system, the
		// programs and every user's files.
		vol := filepath.VolumeName(abs)
		if vol != "" && strings.EqualFold(abs, vol+`\`) {
			return true
		}
		for _, d := range windowsSystemDirs {
			if strings.EqualFold(abs, vol+d) {
				return true
			}
		}
	}
	if home, err := os.UserHomeDir(); err == nil && abs == home {
		return true
	}
	for _, d := range systemDirs {
		if abs == d {
			return true
		}
	}
	return false
}

// fuzzyTarget grades a path token resolvePathArg declined — a glob or an
// unexpanded variable — from the directory the command would run in. From
// inside the workspace it stays ordinary (the prompt sees the raw
// command); from outside, whatever it names lands there too, so it
// escapes; with the directory unknown it cannot be judged at all.
func fuzzyTarget(verb, tk, cwd string, cwdKnown bool, root string) (Risk, string) {
	if root == "" {
		return RiskOrdinary, ""
	}
	if cwdKnown && !outsideWorkspace(cwd, root) {
		return RiskOrdinary, ""
	}
	if !cwdKnown {
		return RiskUnknown, "the working directory is unclear after cd, so " + verb + " " + tk + " cannot be judged"
	}
	return RiskEscapes, verb + " " + tk + " runs against " + cwd + ", outside the workspace"
}

// outsideWorkspace reports whether dir is neither the workspace nor a
// scratch directory under the temp dir.
func outsideWorkspace(dir, root string) bool {
	if dir == "" || root == "" {
		return false
	}
	rc := filepath.Clean(root)
	dc := filepath.Clean(dir)
	if dc == rc || strings.HasPrefix(dc, rc+string(filepath.Separator)) {
		return false
	}
	return !underTemp(dc)
}

// shellVarRe expands the variables resolvePathArg can judge: the home
// directory and the temp dir, in $VAR and ${VAR} forms. Anything else
// ($BUILD_DIR, $(cmd), globs) stays unexpanded and the token is declined.
var shellVarRe = regexp.MustCompile(`\$(?:HOME\b|\{HOME\}|TMPDIR\b|\{TMPDIR\})`)

// resolvePathArg resolves a shell word against cwd (the directory the
// command would run in): ~, $HOME/${HOME}, $TMPDIR/${TMPDIR} expand
// first, then a relative word joins cwd the way the shell would resolve
// it from the working directory. A glob or unexpanded variable is left
// alone since nothing here can judge what it expands to. Shared by
// escapesResolved (which only cares whether the result lands outside
// root) and WriteTargets (which wants the resolved path whichever side
// of root it lands on, bare relative words included: "foo_test.go" is
// exactly the shape a locked file's own name takes in a command run from
// the workspace).
func resolvePathArg(tk, cwd string) (string, bool) {
	if strings.HasPrefix(tk, "~") {
		exp, ok := ExpandTilde(tk)
		if !ok {
			return "", false
		}
		tk = exp
	}
	if strings.Contains(tk, "$") {
		tk = shellVarRe.ReplaceAllStringFunc(tk, func(m string) string {
			if strings.Contains(m, "TMPDIR") {
				return os.TempDir()
			}
			if home, err := os.UserHomeDir(); err == nil {
				return home
			}
			return m
		})
	}
	if strings.ContainsAny(tk, "$*?") {
		return "", false // a glob or variable: cannot judge, leave it to the prompt
	}
	return absFrom(tk, cwd), true
}

// ExpandTilde expands a leading ~ the way the shell does: ~ and ~/x to the
// home directory, ~name/x to that user's home. ~+, ~- and a user that
// cannot be looked up are not judgeable, so ok is false.
func ExpandTilde(tk string) (string, bool) {
	name, rest, _ := strings.Cut(tk[1:], "/")
	if name == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return tk, true // as before: left unexpanded, judged as written
		}
		return home + tk[1:], true
	}
	u, err := user.Lookup(name)
	if err != nil || u.HomeDir == "" {
		return "", false
	}
	if rest == "" {
		return u.HomeDir, true
	}
	return u.HomeDir + "/" + rest, true
}

// WriteTargets returns the file paths a shell command line would write
// to, resolved against root: the path arguments of a mutator like
// mv/cp/tee/rm, plus any redirect target. It shares Classify's wrapper
// unwrapping (sudo, env, timeout, sh -c, eval) and path recognition
// (resolvePathArg), so a caller checking a command against a set of
// protected paths sees exactly the writes Classify would grade — used
// by the accept-tests lock to catch a bash command that would touch a
// locked test file the same way edit_file/write_file are caught.
func WriteTargets(command, root string) []string {
	command = WindowsPaths(command)
	var out []string
	seen := map[string]bool{}
	add := func(p string) {
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	writeTargetsAt(command, root, root, root != "", 0, add)
	return out
}

// writeTargetsAt is WriteTargets for one command line run from cwd, which
// is how a substitution's body is read.
func writeTargetsAt(command, root, cwd string, cwdKnown bool, depth int, add func(string)) {
	if depth > MaxWrapperDepth {
		return
	}
	command = ifsRe.ReplaceAllString(command, " ")
	entryCwd, entryKnown := cwd, cwdKnown
	inCase := false
	for _, seg := range Segments(command) {
		toks := Words(seg)
		if dir, known, isCd := cdTokens(toks, cwd, cwdKnown); isCd {
			cwd, cwdKnown = dir, known
			continue
		}
		collectWriteTargets(seg, toks, root, cwd, cwdKnown, depth, add)
		var arm []string
		if arm, inCase = caseArm(toks, inCase); len(arm) > 0 {
			collectWriteTargets(seg, arm, root, cwd, cwdKnown, depth, add)
		}
	}
	for _, body := range substitutions(command) {
		writeTargetsAt(body, root, entryCwd, entryKnown, depth+1, add)
	}
}

func collectWriteTargets(seg string, toks []string, root string, cwd string, cwdKnown bool, depth int, add func(string)) {
	seg = strings.TrimSpace(seg)
	if seg == "" || depth > MaxWrapperDepth || len(toks) == 0 {
		return
	}
	toks = commandWords(toks)
	if len(toks) == 0 {
		return
	}
	switch toks[0] {
	case "sudo", "doas", "nice", "nohup", "time", "command", "exec", "builtin", "stdbuf", "unbuffer", "timeout", "xargs",
		"setsid", "ionice", "taskset", "chrt", "busybox":
		collectWriteTargets(seg, wrapped(toks), root, cwd, cwdKnown, depth+1, add)
		return
	case "env":
		inner, chdir, _ := envWrapped(toks)
		if chdir != "" {
			if dir, ok := resolvePathArg(chdir, cwd); ok {
				cwd = dir
			}
		}
		collectWriteTargets(seg, inner, root, cwd, cwdKnown, depth+1, add)
		return
	case "sh", "bash", "zsh", "dash", "ksh":
		if line, ok := shellCommand(toks); ok {
			writeTargetsAt(line, root, cwd, cwdKnown, depth+1, add)
		}
		addRedirects(seg, cwd, add)
		return
	case "eval":
		writeTargetsAt(strings.Join(toks[1:], " "), root, cwd, cwdKnown, depth+1, add)
		addRedirects(seg, cwd, add)
		return
	case "find":
		starts, deletes, writes, execs := findCommand(toks)
		for _, ex := range execs {
			var sub []string
			for _, w := range ex {
				if w != "{}" {
					sub = append(sub, w)
				}
			}
			collectWriteTargets(strings.Join(sub, " "), sub, root, cwd, cwdKnown, depth+1, add)
		}
		if deletes {
			writes = append(writes, starts...)
		}
		for _, w := range writes {
			if abs, ok := resolvePathArg(w, cwd); ok {
				add(abs)
			}
		}
	}
	if mutators[toks[0]] {
		for _, tk := range mutatorTargets(toks) {
			if abs, ok := resolvePathArg(tk, cwd); ok {
				add(abs)
			}
		}
	}
	addRedirects(seg, cwd, add)
}

// addRedirects adds the resolved targets of a segment's redirections.
func addRedirects(seg, cwd string, add func(string)) {
	for _, raw := range redirectTargets(seg) {
		tk := strings.Trim(raw, `"'`)
		if nullDevice(tk) {
			continue
		}
		if abs, ok := resolvePathArg(tk, cwd); ok {
			add(abs)
		}
	}
}

// nullDevice reports whether a redirect target is the null device or a
// standard stream: writing there writes no file. On Windows that is NUL
// or $null, and /dev/null too, which a model writes out of habit.
func nullDevice(tk string) bool {
	if tk == "/dev/null" || strings.HasPrefix(tk, "/dev/std") {
		return true
	}
	return runtime.GOOS == "windows" && (strings.EqualFold(tk, "NUL") || strings.EqualFold(tk, "$null"))
}

// SafeRead reports whether a command is a known-safe read: listing,
// printing, grepping, git status/diff/log and the like, with no write
// redirect and no privilege escalation. Used by AUTO mode to skip the
// approval prompt; it is a static allowlist, not a classifier.
func SafeRead(command string) bool { return safeRead(command, 0) }

func safeRead(command string, depth int) bool {
	if strings.TrimSpace(command) == "" || depth > MaxWrapperDepth {
		return false
	}
	if risk, _ := classify(command, "", depth); risk != RiskOrdinary {
		return false
	}
	for _, seg := range Segments(command) {
		if !safeSegment(seg, depth) {
			return false
		}
	}
	// `echo $(rm -rf x)` is an echo whose argument is worked out by
	// running rm: a line is a safe read only if everything it
	// substitutes is one too.
	for _, body := range substitutions(command) {
		if !safeRead(body, depth+1) {
			return false
		}
	}
	return true
}

func safeSegment(seg string, depth int) bool {
	seg = strings.TrimSpace(seg)
	if seg == "" {
		return true
	}
	if depth > MaxWrapperDepth {
		return false
	}
	if writesRedirect(seg) {
		return false
	}
	return safeWords(Words(seg), depth)
}

func writesRedirect(seg string) bool {
	for _, raw := range redirectTargets(seg) {
		if !nullDevice(strings.Trim(raw, `"'`)) {
			return true
		}
	}
	return false
}

func safeWords(toks []string, depth int) bool {
	if len(toks) == 0 {
		return true
	}
	if depth > MaxWrapperDepth {
		return false
	}
	switch toks[0] {
	case "sudo", "doas":
		return false
	case "nice", "nohup", "time", "command", "builtin", "stdbuf", "unbuffer", "timeout":
		if toks[0] == "time" && slices.ContainsFunc(toks, func(t string) bool { return t == "-o" || strings.HasPrefix(t, "--output") }) {
			return false // GNU time -o writes its report to a file
		}
		return safeWords(wrapped(toks), depth+1)
	case "env":
		// A variable changes what the command does — BASH_ENV runs a
		// file before `bash -c`, GIT_EXTERNAL_DIFF runs a program from
		// `git diff`, LD_PRELOAD loads code into anything — so a
		// command env changes the environment of is not a known read,
		// whatever it is.
		inner, chdir, changes := envWrapped(toks)
		if changes || chdir != "" {
			return false
		}
		return safeWords(inner, depth+1)
	case "sh", "bash", "zsh", "dash", "ksh":
		if slices.Contains(toks, "--rcfile") || slices.Contains(toks, "--init-file") {
			return false
		}
		if line, ok := shellCommand(toks); ok {
			return safeRead(line, depth+1)
		}
		return false
	case "git":
		return safeGit(toks[1:])
	case "find":
		for _, t := range toks[1:] {
			switch t {
			case "-delete", "-exec", "-execdir", "-ok", "-okdir",
				"-fprint", "-fprint0", "-fprintf", "-fls":
				return false
			}
		}
		return true
	}
	if !safeBin[toks[0]] {
		return false
	}
	return !writesOrRuns(toks)
}

// writesOrRuns reports whether a command on the safe list is being given
// one of the options that make it write a file or run another program:
// `sort -o f` and `uniq in out` write, `sort --compress-program=x`,
// `rg --pre x` and `ag --pager x` run x, `file -C` compiles a magic file,
// and `date -s` and `hostname name` change the machine. The names are
// safe; these uses of them are not reads.
func writesOrRuns(toks []string) bool {
	args := toks[1:]
	has := func(long string, short byte) bool {
		for _, a := range args {
			if a == "--" {
				return false
			}
			if long != "" && (a == long || strings.HasPrefix(a, long+"=")) {
				return true
			}
			if short != 0 && len(a) > 1 && a[0] == '-' && a[1] != '-' && strings.IndexByte(a[1:], short) >= 0 {
				return true
			}
		}
		return false
	}
	switch toks[0] {
	case "sort":
		return has("--output", 'o') || has("--compress-program", 0)
	case "uniq":
		// uniq's second operand is the file it writes.
		n := 0
		for i := 0; i < len(args); i++ {
			a := args[i]
			switch {
			case a == "-f" || a == "-s" || a == "-w":
				i++
			case strings.HasPrefix(a, "-") && a != "-":
			default:
				n++
			}
		}
		return n > 1
	case "rg":
		// --pre runs a program on every file, and --hostname-bin runs
		// one to name the host for hyperlinks.
		return slices.ContainsFunc(args, func(a string) bool {
			return strings.HasPrefix(a, "--pre") || strings.HasPrefix(a, "--hostname-bin")
		})
	case "ag":
		return has("--pager", 0)
	case "file":
		return has("--compile", 'C')
	case "date":
		return has("--set", 's')
	case "hostname":
		return slices.ContainsFunc(args, func(a string) bool { return !strings.HasPrefix(a, "-") }) ||
			has("--file", 'F') || has("--boot", 'b')
	}
	return false
}

func safeGit(args []string) bool {
	i := 0
	for i < len(args) && strings.HasPrefix(args[i], "-") {
		switch {
		case args[i] == "-c", args[i] == "--config-env", strings.HasPrefix(args[i], "--config-env="), strings.HasPrefix(args[i], "--exec-path"):
			// -c core.fsmonitor=…, core.pager=…, diff.external=… and
			// friends make any subcommand run a program of the
			// caller's choosing.
			return false
		case args[i] == "--git-dir", strings.HasPrefix(args[i], "--git-dir="):
			// The git directory's config is read, and its config can run
			// a program from any subcommand (core.fsmonitor on status) —
			// a bare repository checked into the project is a directory
			// of plain files whose config nobody has looked at.
			return false
		case args[i] == "--work-tree":
			i += 2
		default:
			i++
		}
	}
	if i >= len(args) {
		return true
	}
	sub := args[i]
	if !safeGitSub[sub] {
		return false
	}
	rest := args[i+1:]
	for _, a := range rest {
		// diff, log and show write their output to a file with --output.
		if a == "--output" || strings.HasPrefix(a, "--output=") {
			return false
		}
	}
	switch sub {
	case "branch":
		// `git branch name` creates a branch; a name is only a pattern
		// in list mode, or the argument of a filter.
		list := false
		for j := 0; j < len(rest); j++ {
			a := rest[j]
			switch {
			case a == "-l" || a == "--list":
				list = true
			case slices.Contains([]string{"--contains", "--no-contains", "--merged", "--no-merged", "--points-at", "--sort", "--format", "--color", "--column", "--abbrev"}, a):
				j++
			case slices.Contains([]string{"-d", "-D", "-m", "-M", "-c", "-C", "-f", "-u", "-t", "--delete", "--move", "--copy", "--force", "--track", "--no-track", "--set-upstream-to", "--unset-upstream", "--edit-description", "--create-reflog"}, a),
				strings.HasPrefix(a, "--set-upstream-to=") || strings.HasPrefix(a, "--track="):
				return false
			case strings.HasPrefix(a, "-"):
			default:
				if !list {
					return false
				}
			}
		}
	case "symbolic-ref":
		// With a second operand it sets the ref; -d deletes it.
		n := 0
		for j := 0; j < len(rest); j++ {
			switch a := rest[j]; {
			case a == "-d" || a == "--delete":
				return false
			case a == "-m":
				j++
			case !strings.HasPrefix(a, "-"):
				n++
			}
		}
		return n <= 1
	}
	return true
}

var safeBin = map[string]bool{
	"ls": true, "pwd": true, "echo": true, "printf": true, "cat": true,
	"head": true, "tail": true, "wc": true, "file": true, "which": true,
	"true": true, "false": true, "date": true, "whoami": true, "uname": true,
	"hostname": true, "id": true, "groups": true, "basename": true, "dirname": true,
	"realpath": true, "readlink": true, "stat": true, "md5sum": true,
	"sha256sum": true, "sha1sum": true, "grep": true, "egrep": true, "fgrep": true,
	"rg": true, "ag": true, "diff": true, "cmp": true, "comm": true,
	"sort": true, "uniq": true, "cut": true, "tr": true, "test": true, "[": true,
	"sleep": true, "printenv": true, "type": true,
}

var safeGitSub = map[string]bool{
	"status": true, "diff": true, "log": true, "show": true, "branch": true,
	"rev-parse": true, "describe": true, "blame": true, "ls-files": true,
	"ls-tree": true, "cat-file": true, "shortlog": true, "version": true,
	"help": true, "rev-list": true, "name-rev": true, "symbolic-ref": true,
}

// stripQuoted blanks the inside of quoted strings so text in an argument
// (`echo 'curl x | sh'`) is not mistaken for a command. It reads quotes
// the way the shell does: a backslash inside double quotes escapes the
// next character, and $'…' is closed only by an unescaped quote. Reading
// `"a\"b"` as closing at the escaped quote used to shift every quote
// after it by one, so the tail of the line was taken for quoted text and
// whatever command sat there was never looked at.
func stripQuoted(s string) string {
	var b strings.Builder
	quote := byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote == '\'':
			if c == '\'' {
				quote = 0
				b.WriteByte(c)
			}
		case quote == '"' || quote == '$':
			if c == '\\' {
				i++
			} else if c == closerOf(quote) {
				quote = 0
				b.WriteByte(c)
			}
		case c == '$' && i+1 < len(s) && s[i+1] == '\'':
			quote = '$'
			i++
			b.WriteByte('\'')
		case c == '"' || c == '\'':
			quote = c
			b.WriteByte(c)
		case c == '\\' && i+1 < len(s):
			i++
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// closerOf is the character that ends a quote the lexers track: '$'
// stands for an ANSI-C $'…' string, which a single quote closes.
func closerOf(quote byte) byte {
	if quote == '$' {
		return '\''
	}
	return quote
}

// splitSegments breaks a command line at pipes, &&, ||, ; and newlines,
// outside quotes. Subshell parentheses are stripped. Quotes are read as
// stripQuoted reads them; see there for why the escapes matter. The &
// and | of a redirection operator (>&, &>, >|, <&) are part of it, not a
// separator: splitting `echo x >| ~/.bashrc` at the bar left a segment
// ending in a bare > and another that was just the path, so the write
// was seen by nothing.
func Segments(s string) []string {
	var segs []string
	var cur strings.Builder
	quote := byte(0)
	redir := false // the previous byte was an unquoted, unescaped > or <
	for i := 0; i < len(s); i++ {
		c := s[i]
		afterRedir := redir
		redir = false
		switch {
		case quote == '\'':
			cur.WriteByte(c)
			if c == '\'' {
				quote = 0
			}
		case quote == '"' || quote == '$':
			cur.WriteByte(c)
			if c == '\\' && i+1 < len(s) {
				cur.WriteByte(s[i+1])
				i++
			} else if c == closerOf(quote) {
				quote = 0
			}
		case c == '$' && i+1 < len(s) && s[i+1] == '\'':
			quote = '$'
			cur.WriteString("$'")
			i++
		case c == '"' || c == '\'':
			quote = c
			cur.WriteByte(c)
		case c == '\\' && i+1 < len(s):
			cur.WriteByte(c)
			cur.WriteByte(s[i+1])
			i++
		case c == '>' || c == '<':
			cur.WriteByte(c)
			redir = true
		case (c == '&' || c == '|') && afterRedir, c == '&' && i+1 < len(s) && s[i+1] == '>':
			cur.WriteByte(c)
		case c == ';' || c == '\n' || c == '|' || c == '&':
			segs = append(segs, cur.String())
			cur.Reset()
		case c == '(' || c == ')':
			cur.WriteByte(' ')
		default:
			cur.WriteByte(c)
		}
	}
	segs = append(segs, cur.String())
	return segs
}

// shellWords splits on whitespace honouring quotes; quotes are removed.
// The word is what the shell would pass: inside double quotes a
// backslash escapes only $ ` " \ and a newline, $'…' decodes its C-style
// escapes, $"…" is a double-quoted string, and a backslash-newline joins
// lines. Each of these used to come out differently — `$'\x72m'` as
// "$\x72m", `r\<newline>m` with the newline kept — which is to say as
// some command other than the rm the shell runs.
func Words(s string) []string {
	var words []string
	var cur strings.Builder
	in := false
	quote := byte(0)
	flush := func() {
		if in {
			words = append(words, cur.String())
			cur.Reset()
			in = false
		}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote == '\'':
			if c == quote {
				quote = 0
			} else {
				cur.WriteByte(c)
			}
		case quote == '"':
			switch {
			case c == '"':
				quote = 0
			case c == '\\' && i+1 < len(s) && strings.IndexByte("$`\"\\\n", s[i+1]) >= 0:
				if s[i+1] != '\n' {
					cur.WriteByte(s[i+1])
				}
				i++
			default:
				cur.WriteByte(c)
			}
		case quote == '$':
			switch {
			case c == '\'':
				quote = 0
			case c == '\\' && i+1 < len(s):
				i = ansiEscape(s, i+1, &cur)
			default:
				cur.WriteByte(c)
			}
		case c == '$' && i+1 < len(s) && (s[i+1] == '\'' || s[i+1] == '"'):
			quote = s[i+1]
			if quote == '\'' {
				quote = '$'
			}
			in = true
			i++
		case c == '"' || c == '\'':
			quote = c
			in = true
		case c == '\\' && i+1 < len(s):
			if s[i+1] != '\n' {
				cur.WriteByte(s[i+1])
				in = true
			}
			i++
		case c == ' ' || c == '\t':
			flush()
		default:
			cur.WriteByte(c)
			in = true
		}
	}
	flush()
	return words
}

// ansiSimple are the one-letter escapes of a $'…' string.
var ansiSimple = map[byte]byte{'a': 7, 'b': 8, 'e': 27, 'E': 27, 'f': 12, 'n': '\n', 'r': '\r', 't': '\t', 'v': 11, '\\': '\\', '\'': '\'', '"': '"', '?': '?'}

// ansiEscape decodes the escape of a $'…' string whose letter is at s[j],
// writes the character it stands for, and returns the index of the last
// byte it consumed.
func ansiEscape(s string, j int, b *strings.Builder) int {
	c := s[j]
	if v, ok := ansiSimple[c]; ok {
		b.WriteByte(v)
		return j
	}
	digits := func(from, max, base int) (int, int) {
		n, k := 0, from
		for ; k < len(s) && k < from+max; k++ {
			d := strings.IndexByte("0123456789abcdef", s[k]|0x20)
			if d < 0 || d >= base {
				break
			}
			n = n*base + d
		}
		return n, k
	}
	switch {
	case c >= '0' && c <= '7':
		n, k := digits(j, 3, 8)
		b.WriteByte(byte(n))
		return k - 1
	case c == 'x' || c == 'u' || c == 'U':
		max := map[byte]int{'x': 2, 'u': 4, 'U': 8}[c]
		n, k := digits(j+1, max, 16)
		if k == j+1 {
			break
		}
		if c == 'x' {
			b.WriteByte(byte(n))
		} else {
			b.WriteRune(rune(n))
		}
		return k - 1
	case c == 'c' && j+1 < len(s):
		b.WriteByte(s[j+1] & 0x1f)
		return j + 1
	}
	b.WriteByte('\\')
	b.WriteByte(c)
	return j
}

// cdPrefix matches a leading `cd <dir> &&` or `cd <dir>;` segment.
var cdPrefix = regexp.MustCompile(`^\s*cd\s+("[^"]*"|'[^']*'|[^\s;&|]+)\s*(?:&&|;)\s*`)

// TrimWorkspaceCd removes a leading `cd <dir> &&` (or `;`) segment from
// command when dir is root itself — the shape many models put in front
// of every command, which changes nothing about what runs, since bash
// already starts in root. A cd anywhere else is part of what the command
// does and is left alone.
func TrimWorkspaceCd(command, root string) string {
	m := cdPrefix.FindStringSubmatch(command)
	if m == nil {
		return command
	}
	dir := strings.Trim(m[1], `"'`)
	if dir != "." && dir != "./" {
		if root == "" {
			return command
		}
		dir = absFrom(dir, root)
		if filepath.Clean(dir) != filepath.Clean(root) {
			return command
		}
	}
	return strings.TrimSpace(command[len(m[0]):])
}

// Redirects returns the targets of the output redirections (> and >>) in
// one command segment, as written.
func Redirects(seg string) []string {
	return redirectTargets(seg)
}
