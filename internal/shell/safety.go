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
	for _, seg := range Segments(command) {
		if dir, known, isCd := cdSegment(seg, cwd, cwdKnown); isCd {
			cwd, cwdKnown = dir, known
			continue
		}
		r, why := classifyOne(seg, root, cwd, cwdKnown, depth)
		if r > worst {
			worst, reason = r, why
		}
	}
	return worst, reason
}

// cdSegment parses a segment that is nothing but `cd <dir>` (Segments has
// already split at &&/; so no other command can share the segment) and
// resolves the new working directory. isCd is false for anything else.
// A target that cannot be resolved leaves the directory known=false; a
// target is also unresolvable when the current directory is already
// unknown and the target is relative.
func cdSegment(seg, cwd string, cwdKnown bool) (dir string, known, isCd bool) {
	toks := Words(seg)
	if len(toks) == 0 || toks[0] != "cd" || len(toks) > 2 {
		return "", false, false
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

func classifyOne(seg, root string, cwd string, cwdKnown bool, depth int) (Risk, string) {
	seg = strings.TrimSpace(seg)
	if seg == "" {
		return RiskOrdinary, ""
	}
	if depth > MaxWrapperDepth {
		return RiskUnknown, tooDeep
	}
	return classifyWords(Words(seg), seg, root, cwd, cwdKnown, depth)
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
	// Redirections write wherever they point.
	for _, m := range redirect.FindAllStringSubmatch(seg, -1) {
		tk := strings.Trim(m[1], `"'`)
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

var redirect = regexp.MustCompile(`(?:^|[^<>&|0-9])>{1,2}\s*("[^"]+"|'[^']+'|[^\s;&|]+)`)

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
	cwd, cwdKnown := root, root != ""
	for _, seg := range Segments(command) {
		if dir, known, isCd := cdSegment(seg, cwd, cwdKnown); isCd {
			cwd, cwdKnown = dir, known
			continue
		}
		collectWriteTargets(seg, root, cwd, cwdKnown, 0, add)
	}
	return out
}

func collectWriteTargets(seg, root string, cwd string, cwdKnown bool, depth int, add func(string)) {
	seg = strings.TrimSpace(seg)
	if seg == "" || depth > MaxWrapperDepth {
		return
	}
	toks := Words(seg)
	if len(toks) == 0 {
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
		collectWriteTargets(strings.Join(toks[skipOpts(1):], " "), root, cwd, cwdKnown, depth+1, add)
		return
	case "env":
		i := 1
		for i < len(toks) && (strings.HasPrefix(toks[i], "-") || strings.Contains(toks[i], "=")) {
			i++
		}
		collectWriteTargets(strings.Join(toks[i:], " "), root, cwd, cwdKnown, depth+1, add)
		return
	case "timeout":
		i := skipOpts(1)
		if i < len(toks) {
			i++ // the duration
		}
		collectWriteTargets(strings.Join(toks[i:], " "), root, cwd, cwdKnown, depth+1, add)
		return
	case "sh", "bash", "zsh", "dash", "ksh":
		for i := 1; i < len(toks)-1; i++ {
			if strings.HasPrefix(toks[i], "-") && strings.Contains(toks[i], "c") {
				for _, s := range Segments(toks[i+1]) {
					collectWriteTargets(s, root, cwd, cwdKnown, depth+1, add)
				}
				return
			}
		}
		return
	case "eval":
		for _, s := range Segments(strings.Join(toks[1:], " ")) {
			collectWriteTargets(s, root, cwd, cwdKnown, depth+1, add)
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
	for _, m := range redirect.FindAllStringSubmatch(seg, -1) {
		if abs, ok := resolvePathArg(strings.Trim(m[1], `"'`), cwd); ok {
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
	for _, m := range redirect.FindAllStringSubmatch(seg, -1) {
		p := strings.Trim(m[1], `"'`)
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
// (`echo 'curl x | sh'`) is not mistaken for a command.
func stripQuoted(s string) string {
	var b strings.Builder
	quote := byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
				b.WriteByte(c)
			}
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

// splitSegments breaks a command line at pipes, &&, ||, ; and newlines,
// outside quotes. Subshell parentheses are stripped.
func Segments(s string) []string {
	var segs []string
	var cur strings.Builder
	quote := byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			cur.WriteByte(c)
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
			cur.WriteByte(c)
		case c == '\\' && i+1 < len(s):
			cur.WriteByte(c)
			cur.WriteByte(s[i+1])
			i++
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
		case quote != 0:
			if c == quote {
				quote = 0
			} else {
				cur.WriteByte(c)
			}
		case c == '"' || c == '\'':
			quote = c
			in = true
		case c == '\\' && i+1 < len(s):
			cur.WriteByte(s[i+1])
			in = true
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
	for _, m := range redirect.FindAllStringSubmatch(seg, -1) {
		out = append(out, m[1])
	}
	return out
}
