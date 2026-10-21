package shell

import (
	"net"
	"net/url"
	"path"
	"regexp"
	"strings"
)

// Commands returns every command a line runs, one per entry, as the
// shell would split it: the segments of the line (pipes, &&, ||, ;, &,
// newlines) and, recursively, the segments of every command and process
// substitution it holds. A here-document's body is stdin, not commands,
// so it is taken out first (see StripHeredocs) — unless the command it
// feeds is itself an interpreter (see HeredocRunsCode), in which case
// every line of it is a command and is returned as one.
func Commands(line string) []string {
	var out []string
	var walk func(s string, depth int)
	walk = func(s string, depth int) {
		if depth > MaxWrapperDepth {
			return
		}
		if !HeredocRunsCode(s) {
			s = StripHeredocs(s)
		}
		outer, bodies := SplitSubstitutions(s)
		for _, seg := range Segments(outer) {
			if seg = strings.TrimSpace(seg); seg != "" {
				out = append(out, seg)
			}
		}
		for _, b := range bodies {
			walk(b, depth+1)
		}
	}
	walk(line, 0)
	return out
}

// interpreters read the program they run from stdin when given no
// script, so a here-document fed to one is code rather than data. ssh
// runs its stdin on the remote host.
var interpreters = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true, "ash": true, "fish": true, "mksh": true,
	"python": true, "python3": true, "python2": true, "perl": true, "ruby": true, "node": true, "php": true,
	"lua": true, "tclsh": true, "ssh": true, "eval": true, "source": true, ".": true, "osascript": true, "pwsh": true,
}

// HeredocRunsCode reports whether a here-document on the line feeds a
// command that runs what it reads — `bash <<EOF`, `ssh host <<EOF`,
// `python3 - <<EOF` — so that its body has to be judged as commands.
func HeredocRunsCode(line string) bool {
	if !strings.Contains(line, "<<") {
		return false
	}
	lines := []string{line}
	for i := 0; i < len(lines) && i < 64; i++ {
		outer, bodies := SplitSubstitutions(lines[i])
		lines = append(lines, bodies...)
		for _, seg := range Segments(outer) {
			if !strings.Contains(seg, "<<") {
				continue
			}
			if toks := ArgvOf(seg); len(toks) > 0 && interpreters[path.Base(toks[0])] {
				return true
			}
		}
	}
	return false
}

// ArgvOf is one command's words the way the command sees them: quotes
// removed, leading VAR=value assignments dropped, a path to the program
// reduced to its name (/usr/bin/rm is rm), and wrappers that only run
// another command (sudo, env, nice, timeout, xargs, nohup, command,
// exec, time, stdbuf, doas) looked through, so `sudo -u root rm -rf x`
// is rm. `sh -c '…'` is not unwrapped here — its argument is a command
// line of its own; Argvs follows it.
func ArgvOf(seg string) []string {
	toks := Words(strings.TrimSpace(seg))
	for depth := 0; depth <= MaxWrapperDepth; depth++ {
		toks = commandWords(toks)
		if len(toks) == 0 {
			return nil
		}
		switch toks[0] {
		case "sudo", "doas", "nice", "timeout", "xargs", "time", "stdbuf", "exec", "setsid", "ionice", "taskset", "chrt", "busybox":
			toks = wrapped(toks)
		case "nohup", "command", "builtin":
			toks = toks[1:]
		case "env":
			toks, _, _ = envWrapped(toks)
		default:
			return toks
		}
	}
	return toks
}

// RawArgv is one command's words with quotes removed and nothing else
// changed: what an allow rule is matched against, since an allow must
// not see through a wrapper (sudo go test is not go test).
func RawArgv(seg string) []string { return Words(strings.TrimSpace(seg)) }

// Argvs is every command a line runs as its argv (see ArgvOf), with the
// command line inside a `sh -c` followed as well.
func Argvs(line string) [][]string {
	var out [][]string
	var walk func(l string, depth int)
	walk = func(l string, depth int) {
		if depth > MaxWrapperDepth {
			return
		}
		for _, seg := range Commands(l) {
			argv := ArgvOf(seg)
			if len(argv) == 0 {
				continue
			}
			out = append(out, argv)
			if inner, ok := shellCommand(argv); ok && interpreters[argv[0]] {
				walk(inner, depth+1)
			}
		}
	}
	walk(line, 0)
	return out
}

// schemeURL is a URL written with a scheme a network tool understands.
var schemeURL = regexp.MustCompile(`(?i)\b(?:https?|ftp|ftps|git|ssh|wss?|rsync|sftp|scp)://[^\s'"<>|;&)]+`)

// scpLike is user@host:path, the address form of scp, rsync and git.
var scpLike = regexp.MustCompile(`^(?:[A-Za-z0-9._-]+@)?([A-Za-z0-9.-]+\.[A-Za-z]{2,}|[A-Za-z0-9.-]+@?):`)

// netCommands take a host as an argument, and say where: "url" commands
// (curl, wget) take any number of addresses, each of which may be a bare
// host; "first" commands (ssh, nc, ping) take the host as their first
// argument and something else after it; "copy" commands (scp, rsync)
// name a remote only as host:path, every other word being a local file.
var netCommands = map[string]string{
	"curl": "url", "wget": "url", "http": "url", "https": "url", "xh": "url", "aria2c": "url",
	"ssh": "first", "sftp": "first", "nc": "first", "ncat": "first", "netcat": "first", "telnet": "first",
	"ping": "first", "ping6": "first", "dig": "first", "nslookup": "first", "host": "first", "ftp": "first", "mosh": "first",
	"scp": "copy", "rsync": "copy",
}

// hostFlags take the next word as something other than a host.
var hostArgFlags = map[string]bool{
	"-o": true, "-O": true, "-H": true, "-d": true, "--data": true, "-X": true, "-p": true, "-P": true, "-i": true, "-l": true,
	"-u": true, "-e": true, "-F": true, "-A": true, "-b": true, "-c": true, "-w": true, "-T": true, "-m": true, "-r": true,
	"--header": true, "--output": true, "--user-agent": true, "--cookie": true, "--form": true, "-L": false,
}

// NetworkHosts returns the hosts a command line would contact, as far as
// they can be told from the words: every URL with a scheme anywhere on
// the line, the host of a git remote given as an address (git clone
// git@github.com:x/y), and the host argument of ssh, scp, rsync, curl,
// wget, nc and the like. Lower-cased, port removed, in first-seen order.
// A command that reaches the network through a name it does not show
// (git push to a configured remote) has no host here.
func NetworkHosts(line string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(h string) {
		h = strings.ToLower(strings.TrimSuffix(strings.Trim(h, "[]"), "."))
		if hh, _, err := net.SplitHostPort(h); err == nil {
			h = strings.Trim(hh, "[]")
		}
		if h == "" || seen[h] {
			return
		}
		seen[h] = true
		out = append(out, h)
	}
	for _, m := range schemeURL.FindAllString(line, -1) {
		if u, err := url.Parse(m); err == nil {
			add(u.Hostname())
		}
	}
	for _, argv := range Argvs(line) {
		name := path.Base(argv[0])
		switch {
		case name == "git":
			for _, a := range argv[1:] {
				if m := scpLike.FindStringSubmatch(a); m != nil && !strings.Contains(a, "://") {
					add(strings.TrimSuffix(m[1], "@"))
				}
			}
		case netCommands[name] != "":
			kind := netCommands[name]
			for i := 1; i < len(argv); i++ {
				a := argv[i]
				if strings.HasPrefix(a, "-") {
					if hostArgFlags[a] {
						i++
					}
					continue
				}
				if strings.Contains(a, "://") {
					continue // counted above
				}
				switch kind {
				case "copy":
					if m := scpLike.FindStringSubmatch(a); m != nil && strings.Contains(a, ":") {
						add(strings.TrimSuffix(m[1], "@"))
					}
					continue
				case "first":
					a = strings.TrimPrefix(a, "@") // dig @server
				}
				if m := scpLike.FindStringSubmatch(a); m != nil {
					add(strings.TrimSuffix(m[1], "@"))
				} else if u, h, ok := strings.Cut(a, "@"); ok && u != "" && looksLikeHost(h) {
					add(h)
				} else if looksLikeHost(a) {
					add(a)
				}
				if kind == "first" {
					break
				}
			}
		}
	}
	return out
}

// looksLikeHost is a word shaped like a host name or an address: a dotted
// name with a letter-only last label, localhost, or an IP.
func looksLikeHost(w string) bool {
	if w == "localhost" {
		return true
	}
	h := w
	if hh, _, err := net.SplitHostPort(w); err == nil {
		h = hh
	} else if i := strings.IndexByte(w, '/'); i > 0 {
		h = w[:i]
	}
	if net.ParseIP(strings.Trim(h, "[]")) != nil {
		return true
	}
	labels := strings.Split(h, ".")
	if len(labels) < 2 {
		return false
	}
	last := labels[len(labels)-1]
	if len(last) < 2 {
		return false
	}
	for _, r := range last {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return false
		}
	}
	for _, l := range labels {
		if l == "" {
			return false
		}
		for _, r := range l {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
				return false
			}
		}
	}
	return true
}

// PathWords returns the words of a command line that name files: every
// argument of every command that looks like a path (it holds a / or a
// dot-name such as .env, or names an existing-looking file with an
// extension), plus redirect targets. It is broad on purpose — it feeds
// the "touches" permission scope, which a deny rule uses to keep a file
// away from every tool, and there a false positive asks where a false
// negative would leak.
func PathWords(line string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(p string) {
		p = strings.Trim(p, `"'`)
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	for _, seg := range Commands(line) {
		for _, t := range redirectTargets(seg) {
			add(t)
		}
		argv := RawArgv(seg)
		for i, a := range argv {
			if i == 0 && !strings.Contains(a, "/") {
				continue
			}
			if strings.HasPrefix(a, "-") {
				// --file=.env
				if _, v, ok := strings.Cut(a, "="); ok {
					a = v
				} else {
					continue
				}
			}
			// curl -d @file, gcc @args: the word after @ is a file.
			a = strings.TrimPrefix(strings.TrimLeft(a, "<>"), "@")
			if strings.ContainsAny(a, "/.~") && !strings.Contains(a, "://") {
				add(a)
			}
		}
	}
	return out
}
