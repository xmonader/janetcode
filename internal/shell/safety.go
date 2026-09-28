package shell

import (
	"os"
	"path/filepath"
	"regexp"
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
	// RiskEscapes mutates paths outside the workspace root.
	RiskEscapes
	// RiskCatastrophic is never run, even with auto-approve.
	RiskCatastrophic
)

func (r Risk) String() string {
	switch r {
	case RiskUnknown:
		return "too deeply nested to classify"
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
	{regexp.MustCompile(`\b(?:curl|wget)\b[^|]*\|\s*(?:sudo\s+)?(?:ba|z|da)?sh\b`), "pipes a download into a shell"},
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
	"sed": true, "perl": true,
}

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
	return classify(command, root, 0)
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
	for _, seg := range Segments(command) {
		toks := Words(seg)
		if dir, known, isCd := cdTokens(toks, cwd, cwdKnown); isCd {
			cwd, cwdKnown, moved = dir, known, true
			continue
		}
		note(classifyOne(seg, toks, root, cwd, cwdKnown, depth))
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

// substitutions returns the bodies of the command and process
// substitutions in s — $(…), `…`, <(…) and >(…) — outside single quotes.
// Double quotes do not stop them: `echo "$(rm -rf x)"` runs the rm. The
// bodies are not searched here for substitutions of their own; judging a
// body as a command line does that.
func substitutions(s string) []string {
	var out []string
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
			out = append(out, s[i+1:end])
			i = end
		case c == '$' && strings.HasPrefix(s[i+1:], "(("):
			// Arithmetic. Its text can still hold a substitution, which
			// the scan finds as it carries on.
			i += 2
		case (c == '$' || (quote == 0 && (c == '<' || c == '>'))) && i+1 < len(s) && s[i+1] == '(':
			end := closeParen(s, i+2)
			out = append(out, s[i+2:end])
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
	if len(toks) == 0 || toks[0] != "cd" {
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
	if !cwdKnown && !filepath.IsAbs(tk) && !strings.HasPrefix(tk, "~") && !strings.HasPrefix(tk, "$HOME") {
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
	if len(toks) == 0 {
		return RiskOrdinary, ""
	}
	if depth > MaxWrapperDepth {
		return RiskUnknown, tooDeep
	}
	skipOpts := func(i int) int {
		for i < len(toks) && strings.HasPrefix(toks[i], "-") {
			i++
		}
		return i
	}
	switch toks[0] {
	case "sudo", "doas", "nice", "nohup", "time", "command", "exec", "builtin", "stdbuf", "unbuffer":
		return classifyWords(toks[skipOpts(1):], seg, root, cwd, cwdKnown, depth+1)
	case "env":
		i := 1
		for i < len(toks) && (strings.HasPrefix(toks[i], "-") || strings.Contains(toks[i], "=")) {
			i++
		}
		return classifyWords(toks[i:], seg, root, cwd, cwdKnown, depth+1)
	case "timeout":
		i := skipOpts(1)
		if i < len(toks) {
			i++ // the duration
		}
		return classifyWords(toks[i:], seg, root, cwd, cwdKnown, depth+1)
	case "sh", "bash", "zsh", "dash", "ksh":
		for i := 1; i < len(toks)-1; i++ {
			if strings.HasPrefix(toks[i], "-") && strings.Contains(toks[i], "c") {
				// The wrapped command line starts in the same directory
				// the wrapper segment runs in.
				return classifyAt(toks[i+1], root, cwd, cwdKnown, depth+1)
			}
		}
	case "eval":
		return classifyAt(strings.Join(toks[1:], " "), root, cwd, cwdKnown, depth+1)
	}
	line := strings.Join(toks, " ")
	for _, c := range catastrophic {
		if c.re.MatchString(line) {
			return RiskCatastrophic, c.reason
		}
	}
	if mutators[toks[0]] {
		for _, tk := range toks[1:] {
			if strings.HasPrefix(tk, "-") {
				continue
			}
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
	// Redirections write wherever they point.
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

// isAbsoluteWord reports whether a path token names a fixed location
// whatever the working directory is: an absolute path, one anchored at
// ~, or one whose variables expand to an absolute prefix ($HOME/x, ${TMPDIR}).
func isAbsoluteWord(tk string) bool {
	if filepath.IsAbs(tk) || strings.HasPrefix(tk, "~") {
		return true
	}
	return filepath.IsAbs(shellVarRe.ReplaceAllString(tk, "/"))
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
	if filepath.IsAbs(tk) {
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

func catastrophicTarget(abs string) bool {
	if abs == "/" {
		return true
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
		if home, err := os.UserHomeDir(); err == nil {
			tk = home + tk[1:]
		}
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
	abs := tk
	if !filepath.IsAbs(tk) {
		abs = filepath.Join(cwd, tk)
	}
	return filepath.Clean(abs), true
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
	entryCwd, entryKnown := cwd, cwdKnown
	for _, seg := range Segments(command) {
		toks := Words(seg)
		if dir, known, isCd := cdTokens(toks, cwd, cwdKnown); isCd {
			cwd, cwdKnown = dir, known
			continue
		}
		collectWriteTargets(seg, toks, root, cwd, cwdKnown, depth, add)
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
	skipOpts := func(i int) int {
		for i < len(toks) && strings.HasPrefix(toks[i], "-") {
			i++
		}
		return i
	}
	switch toks[0] {
	case "sudo", "doas", "nice", "nohup", "time", "command", "exec", "builtin", "stdbuf", "unbuffer":
		collectWriteTargets(strings.Join(toks[skipOpts(1):], " "), toks[skipOpts(1):], root, cwd, cwdKnown, depth+1, add)
		return
	case "env":
		i := 1
		for i < len(toks) && (strings.HasPrefix(toks[i], "-") || strings.Contains(toks[i], "=")) {
			i++
		}
		collectWriteTargets(strings.Join(toks[i:], " "), toks[i:], root, cwd, cwdKnown, depth+1, add)
		return
	case "timeout":
		i := skipOpts(1)
		if i < len(toks) {
			i++ // the duration
		}
		collectWriteTargets(strings.Join(toks[i:], " "), toks[i:], root, cwd, cwdKnown, depth+1, add)
		return
	case "sh", "bash", "zsh", "dash", "ksh":
		for i := 1; i < len(toks)-1; i++ {
			if strings.HasPrefix(toks[i], "-") && strings.Contains(toks[i], "c") {
				for _, s := range Segments(toks[i+1]) {
					collectWriteTargets(s, Words(s), root, cwd, cwdKnown, depth+1, add)
				}
				return
			}
		}
		return
	case "eval":
		for _, s := range Segments(strings.Join(toks[1:], " ")) {
			collectWriteTargets(s, Words(s), root, cwd, cwdKnown, depth+1, add)
		}
		return
	}
	if mutators[toks[0]] {
		for _, tk := range toks[1:] {
			if strings.HasPrefix(tk, "-") {
				continue
			}
			if abs, ok := resolvePathArg(tk, cwd); ok {
				add(abs)
			}
		}
	}
	for _, raw := range redirectTargets(seg) {
		if abs, ok := resolvePathArg(strings.Trim(raw, `"'`), cwd); ok {
			add(abs)
		}
	}
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
		p := strings.Trim(raw, `"'`)
		if p != "/dev/null" && !strings.HasPrefix(p, "/dev/std") {
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
	skipOpts := func(i int) int {
		for i < len(toks) && strings.HasPrefix(toks[i], "-") {
			i++
		}
		return i
	}
	switch toks[0] {
	case "sudo", "doas":
		return false
	case "nice", "nohup", "time", "command", "builtin", "stdbuf", "unbuffer":
		return safeWords(toks[skipOpts(1):], depth+1)
	case "env":
		i := 1
		for i < len(toks) && (strings.HasPrefix(toks[i], "-") || strings.Contains(toks[i], "=")) {
			i++
		}
		if i >= len(toks) {
			return true
		}
		return safeWords(toks[i:], depth+1)
	case "timeout":
		i := skipOpts(1)
		if i < len(toks) {
			i++
		}
		return safeWords(toks[i:], depth+1)
	case "sh", "bash", "zsh", "dash", "ksh":
		for i := 1; i < len(toks)-1; i++ {
			if strings.HasPrefix(toks[i], "-") && strings.Contains(toks[i], "c") {
				return safeRead(toks[i+1], depth+1)
			}
		}
		return false
	case "git":
		return safeGit(toks[1:])
	case "find":
		for _, t := range toks[1:] {
			switch t {
			case "-delete", "-exec", "-execdir", "-ok", "-okdir":
				return false
			}
		}
		return true
	}
	return safeBin[toks[0]]
}

func safeGit(args []string) bool {
	i := 0
	for i < len(args) && strings.HasPrefix(args[i], "-") {
		switch args[i] {
		case "-c", "--git-dir", "--work-tree":
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
	if sub == "branch" {
		for _, a := range args[i+1:] {
			switch a {
			case "-d", "-D", "-m", "-M", "--delete", "--move":
				return false
			}
		}
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
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(root, dir)
		}
		if filepath.Clean(dir) != filepath.Clean(root) {
			return command
		}
	}
	return strings.TrimSpace(command[len(m[0]):])
}

// Redirects returns the targets of the output redirections (> and >>) in
// one command segment, as written.
func Redirects(seg string) []string {
	var out []string
	for _, raw := range redirectTargets(seg) {
		out = append(out, raw)
	}
	return out
}
