package tool

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
	// RiskEscapes mutates paths outside the workspace root.
	RiskEscapes
	// RiskCatastrophic is never run, even with auto-approve.
	RiskCatastrophic
)

func (r Risk) String() string {
	switch r {
	case RiskEscapes:
		return "escapes workspace"
	case RiskCatastrophic:
		return "catastrophic"
	}
	return "ordinary"
}

// maxWrapperDepth bounds unwrapping of sudo/env/sh -c/eval nests.
const maxWrapperDepth = 4

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
var mutators = map[string]bool{
	"rm": true, "rmdir": true, "mv": true, "cp": true, "chmod": true, "chown": true, "chgrp": true,
	"truncate": true, "tee": true, "ln": true, "install": true, "rsync": true, "shred": true, "mkdir": true, "touch": true,
}

// Classify grades a command line run with root as the working directory.
// The reason describes the first problem found.
func Classify(command, root string) (Risk, string) {
	unquoted := stripQuoted(command)
	for _, c := range wholeLine {
		if c.re.MatchString(unquoted) {
			return RiskCatastrophic, c.reason
		}
	}
	worst, reason := RiskOrdinary, ""
	for _, seg := range splitSegments(command) {
		r, why := classifyOne(seg, root, 0)
		if r > worst {
			worst, reason = r, why
		}
	}
	return worst, reason
}

func classifyOne(seg, root string, depth int) (Risk, string) {
	seg = strings.TrimSpace(seg)
	if seg == "" || depth > maxWrapperDepth {
		return RiskOrdinary, ""
	}
	return classifyWords(shellWords(seg), seg, root, depth)
}

// classifyWords peels wrappers so `sudo sh -c "rm -rf /"` is judged by what
// it runs. seg is the raw text, for redirection checks.
func classifyWords(toks []string, seg, root string, depth int) (Risk, string) {
	if len(toks) == 0 || depth > maxWrapperDepth {
		return RiskOrdinary, ""
	}
	skipOpts := func(i int) int {
		for i < len(toks) && strings.HasPrefix(toks[i], "-") {
			i++
		}
		return i
	}
	switch toks[0] {
	case "sudo", "doas", "nice", "nohup", "time", "command", "exec", "builtin", "stdbuf", "unbuffer":
		return classifyWords(toks[skipOpts(1):], seg, root, depth+1)
	case "env":
		i := 1
		for i < len(toks) && (strings.HasPrefix(toks[i], "-") || strings.Contains(toks[i], "=")) {
			i++
		}
		return classifyWords(toks[i:], seg, root, depth+1)
	case "timeout":
		i := skipOpts(1)
		if i < len(toks) {
			i++ // the duration
		}
		return classifyWords(toks[i:], seg, root, depth+1)
	case "sh", "bash", "zsh", "dash", "ksh":
		for i := 1; i < len(toks)-1; i++ {
			if strings.HasPrefix(toks[i], "-") && strings.Contains(toks[i], "c") {
				return Classify(toks[i+1], root)
			}
		}
	case "eval":
		return Classify(strings.Join(toks[1:], " "), root)
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
			if p, out := escapes(tk, root); out {
				return RiskEscapes, toks[0] + " touches " + p + ", outside the workspace"
			}
		}
	}
	// Redirections write wherever they point.
	for _, m := range redirect.FindAllStringSubmatch(seg, -1) {
		if p, out := escapes(strings.Trim(m[1], `"'`), root); out {
			return RiskEscapes, "writes to " + p + ", outside the workspace"
		}
	}
	return RiskOrdinary, ""
}

var redirect = regexp.MustCompile(`(?:^|[^<>&|0-9])>{1,2}\s*("[^"]+"|'[^']+'|[^\s;&|]+)`)

// escapes reports whether a path argument resolves outside root. Only
// tokens that look like paths are considered, so option-like words and
// bare names never trip it.
func escapes(tk, root string) (string, bool) {
	if root == "" {
		return "", false
	}
	if strings.HasPrefix(tk, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			tk = home + tk[1:]
		}
	}
	if strings.HasPrefix(tk, "$HOME") {
		if home, err := os.UserHomeDir(); err == nil {
			tk = home + strings.TrimPrefix(tk, "$HOME")
		}
	}
	if !strings.HasPrefix(tk, "/") && !strings.Contains(tk, "..") {
		return "", false
	}
	if strings.ContainsAny(tk, "$*?") {
		return "", false // a glob or variable: cannot judge, leave it to the prompt
	}
	abs := tk
	if !filepath.IsAbs(tk) {
		abs = filepath.Join(root, tk)
	}
	abs = filepath.Clean(abs)
	rootClean := filepath.Clean(root)
	if abs == rootClean || strings.HasPrefix(abs, rootClean+string(filepath.Separator)) {
		return "", false
	}
	if abs == "/dev/null" || strings.HasPrefix(abs, "/dev/std") {
		return "", false
	}
	// Scratch files named explicitly under the temp dir are fine; reaching
	// it through .. from a workspace that lives there is not.
	if strings.HasPrefix(tk, "/tmp/") || strings.HasPrefix(tk, os.TempDir()+string(filepath.Separator)) {
		return "", false
	}
	return abs, true
}

// SafeRead reports whether a command is a known-safe read: listing,
// printing, grepping, git status/diff/log and the like, with no write
// redirect and no privilege escalation. Used by AUTO mode to skip the
// approval prompt; it is a static allowlist, not a classifier.
func SafeRead(command string) bool {
	if strings.TrimSpace(command) == "" {
		return false
	}
	if risk, _ := Classify(command, ""); risk != RiskOrdinary {
		return false
	}
	for _, seg := range splitSegments(command) {
		if !safeSegment(seg, 0) {
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
	if depth > maxWrapperDepth {
		return false
	}
	if writesRedirect(seg) {
		return false
	}
	return safeWords(shellWords(seg), depth)
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
	if depth > maxWrapperDepth {
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
				return SafeRead(toks[i+1])
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
func splitSegments(s string) []string {
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
func shellWords(s string) []string {
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
