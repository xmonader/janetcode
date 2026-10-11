package taint

import (
	"encoding/json"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/xmonader/janetcode/internal/egress"
	"github.com/xmonader/janetcode/internal/shell"
)

// depSegments are the directory names that hold someone else's code: a
// path through one of them is a dependency, not the user's repository.
var depSegments = map[string]bool{
	"vendor": true, "node_modules": true, "third_party": true, "third-party": true,
	"bower_components": true, "site-packages": true, "dist-packages": true,
	".venv": true, "venv": true, "Pods": true, ".bundle": true,
}

// depInfixes are the shared caches whose paths are only recognisable by
// two segments together: the Go module cache, cargo's registry, Maven's.
var depInfixes = []string{"/pkg/mod/", "/.cargo/registry/", "/.m2/repository/", "/.gradle/caches/"}

// DependencyPath reports whether p is inside a dependency or vendor
// directory or a package manager's cache.
func DependencyPath(p string) bool {
	if p == "" {
		return false
	}
	s := "/" + strings.Trim(filepath.ToSlash(p), "/") + "/"
	for _, inf := range depInfixes {
		if strings.Contains(s, inf) {
			return true
		}
	}
	for _, seg := range strings.Split(s, "/") {
		if depSegments[seg] {
			return true
		}
	}
	return false
}

// Untrusted reports whether a tool result carries content from outside
// the user's own repository and prompt, and names its source. Errors
// count too: a fetched 404 page is still somebody else's text.
//
//   - fetch_url and web_search: the web.
//   - an MCP tool (mcp__server__tool): the server's answer, which is
//     often an issue, a page or a document someone else wrote.
//   - read_file, read_many_files and grep on a dependency path.
//   - bash that fetches (curl, wget), prints an issue or pull request
//     (gh issue view, gh pr view, gh api), or reads a dependency path.
func Untrusted(name string, input json.RawMessage, output string) (source string, ok bool) {
	switch {
	case name == "fetch_url":
		return "fetch_url " + clip(field(input, "url"), 120), true
	case name == "web_search":
		return "web_search " + clip(field(input, "query"), 80), true
	case strings.HasPrefix(name, "mcp__"):
		return "MCP tool " + name, true
	case name == "read_file":
		if p := field(input, "path"); DependencyPath(p) {
			return p, true
		}
	case name == "read_many_files":
		var in struct {
			Paths []string `json:"paths"`
		}
		_ = json.Unmarshal(input, &in)
		for _, p := range in.Paths {
			if DependencyPath(p) {
				return p, true
			}
		}
	case name == "grep":
		if p := field(input, "path"); DependencyPath(p) {
			return "grep in " + p, true
		}
		// A search over the whole tree can still match inside vendor/:
		// each match line starts with the file it is in.
		for _, line := range strings.Split(output, "\n") {
			if f, _, cut := strings.Cut(line, ":"); cut && DependencyPath(f) {
				return "grep match in " + f, true
			}
		}
	case name == "bash":
		return untrustedCommand(field(input, "command"))
	}
	return "", false
}

// readVerbs are the commands that print a file they are given.
var readVerbs = map[string]bool{
	"cat": true, "head": true, "tail": true, "less": true, "more": true, "sed": true, "awk": true,
	"grep": true, "rg": true, "bat": true, "jq": true, "strings": true, "nl": true, "xxd": true,
}

// untrustedCommand is Untrusted for one bash command line.
func untrustedCommand(cmd string) (string, bool) {
	for _, argv := range shell.Argvs(cmd) {
		if len(argv) == 0 {
			continue
		}
		verb := argv[0]
		switch {
		case verb == "curl" || verb == "wget" || verb == "http" || verb == "https" || verb == "xh":
			if hosts := remoteHosts(cmd); len(hosts) > 0 {
				return verb + " " + hosts[0], true
			}
		case verb == "gh" && len(argv) > 1 && (argv[1] == "issue" || argv[1] == "pr" || argv[1] == "api" || argv[1] == "release" || argv[1] == "gist"):
			return "gh " + argv[1] + " (text from GitHub)", true
		case verb == "glab" && len(argv) > 1 && (argv[1] == "issue" || argv[1] == "mr" || argv[1] == "api"):
			return "glab " + argv[1] + " (text from GitLab)", true
		case readVerbs[verb]:
			for _, a := range argv[1:] {
				if !strings.HasPrefix(a, "-") && DependencyPath(a) {
					return a, true
				}
			}
		}
	}
	return "", false
}

// remoteHosts are the hosts a command line spells out, loopback excluded:
// a request to a server the turn started itself is not egress.
func remoteHosts(cmd string) []string {
	var out []string
	for _, h := range egress.HostsInCommand(cmd) {
		if h == "localhost" || strings.HasPrefix(h, "127.") || h == "::1" || h == "0.0.0.0" {
			continue
		}
		out = append(out, h)
	}
	return out
}

// netVerbs are commands whose job is to reach another machine.
var netVerbs = map[string]bool{
	"curl": true, "wget": true, "nc": true, "ncat": true, "netcat": true, "socat": true, "telnet": true,
	"ssh": true, "scp": true, "sftp": true, "rsync": true, "ftp": true, "http": true, "https": true,
	"xh": true, "aria2c": true, "gh": true, "glab": true, "twine": true,
}

// netSubcommands are the subcommands of tools that are local otherwise
// but reach a registry or a remote for these.
var netSubcommands = map[string]map[string]bool{
	"git":      {"push": true, "fetch": true, "pull": true, "clone": true, "ls-remote": true, "send-email": true, "submodule": true},
	"npm":      {"install": true, "i": true, "add": true, "ci": true, "publish": true, "update": true, "exec": true},
	"npx":      {"*": true},
	"pnpm":     {"install": true, "i": true, "add": true, "publish": true, "update": true, "dlx": true},
	"yarn":     {"install": true, "add": true, "publish": true, "upgrade": true, "dlx": true},
	"bun":      {"install": true, "add": true, "publish": true, "x": true},
	"pip":      {"install": true, "download": true},
	"pip3":     {"install": true, "download": true},
	"uv":       {"pip": true, "add": true, "sync": true, "publish": true},
	"poetry":   {"add": true, "install": true, "publish": true, "update": true},
	"go":       {"get": true, "install": true, "mod": true},
	"cargo":    {"install": true, "add": true, "publish": true, "update": true, "fetch": true},
	"gem":      {"install": true, "push": true},
	"composer": {"require": true, "install": true, "update": true},
	"docker":   {"push": true, "pull": true, "login": true},
}

// inlineNet are the spellings of a network call inside an interpreter's
// inline program (python -c, node -e) that names no URL.
var inlineNet = regexp.MustCompile(`urllib|requests\.|http\.client|socket\.|fetch\(|https?\.request|net\.Dial|XMLHttpRequest`)

// EgressCommand reports whether a bash command line can send data off the
// machine, and returns the arguments a prompt can trace back to the
// untrusted text: the hosts and URLs it names and, for an install, the
// package names. It is recognition, not proof — a command can reach a
// host it does not spell — which is why the egress proxy and the sandbox
// are what enforce, and this only decides when to ask.
func EgressCommand(cmd string) (needles []string, ok bool) {
	// A here-document fed to cat is a file being written: the URLs in a
	// Go source it holds are not requests. One fed to an interpreter or
	// to ssh is code, and stays.
	if !shell.HeredocRunsCode(cmd) {
		cmd = shell.StripHeredocs(cmd)
	}
	needles = append(needles, urlsIn(cmd)...)
	hosts := remoteHosts(cmd)
	needles = append(needles, hosts...)
	ok = len(hosts) > 0
	// curl to a server the turn started itself is not egress: a command
	// that spells hosts, all of them loopback, is local.
	local := len(hosts) == 0 && len(egress.HostsInCommand(cmd)) > 0
	for _, argv := range shell.Argvs(cmd) {
		if len(argv) == 0 {
			continue
		}
		verb := argv[0]
		if verb == "python" || verb == "python3" || verb == "node" || verb == "ruby" || verb == "perl" {
			if inlineNet.MatchString(cmd) {
				ok = true
			}
			if len(argv) > 3 && argv[1] == "-m" && argv[2] == "pip" && argv[3] == "install" {
				ok = true
				needles = append(needles, packageArgs(argv[4:])...)
			}
			continue
		}
		if netVerbs[verb] {
			ok = ok || !local
			continue
		}
		subs := netSubcommands[verb]
		if subs == nil {
			continue
		}
		if subs["*"] {
			ok = true
			needles = append(needles, packageArgs(argv[1:])...)
			continue
		}
		sub := firstWord(argv[1:])
		if verb == "go" && sub == "mod" {
			// go mod init, edit, graph and why stay local; download
			// and tidy fetch modules.
			if i := indexOf(argv, "mod"); i+1 < len(argv) && (argv[i+1] == "download" || argv[i+1] == "tidy") {
				ok = true
			}
			continue
		}
		if subs[sub] {
			ok = true
			needles = append(needles, packageArgs(argv[2:])...)
		}
	}
	return dedupe(needles), ok
}

// valueFlags are the global flags that take the next word as their
// value, so the subcommand is the word after it: git -C dir push.
var valueFlags = map[string]bool{"-C": true, "-c": true, "--git-dir": true, "--work-tree": true, "--prefix": true, "--cwd": true}

// firstWord is the first argument that is not a flag or a flag's value.
func firstWord(args []string) string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if valueFlags[a] {
			i++
			continue
		}
		if !strings.HasPrefix(a, "-") {
			return a
		}
	}
	return ""
}

func indexOf(xs []string, s string) int {
	for i, x := range xs {
		if x == s {
			return i
		}
	}
	return -1
}

// packageArgs are the non-flag arguments of an install: the names an
// injection would have planted.
func packageArgs(args []string) []string {
	var out []string
	for _, a := range args {
		if a != "" && !strings.HasPrefix(a, "-") && a != "." {
			out = append(out, a)
		}
	}
	return out
}

var urlRe = regexp.MustCompile(`(?i)\b(?:https?|ftp|wss?)://[^\s'"<>()]+`)

// urlsIn lists the URLs in s.
func urlsIn(s string) []string { return urlRe.FindAllString(s, -1) }

// Needles are what a prompt can trace back to untrusted text in an edit's
// content: the URLs it writes.
func Needles(content string) []string { return dedupe(urlsIn(content)) }

func dedupe(xs []string) []string {
	var out []string
	for _, x := range xs {
		if x != "" && !contains(out, x) {
			out = append(out, x)
		}
	}
	return out
}

// ciPatterns are the files that decide what CI runs, with what secrets.
// Matched against a path relative to the repository, slash-separated.
var ciPatterns = []string{
	".github/workflows/*", ".github/actions/*", ".gitlab-ci.yml", ".gitlab/*",
	".circleci/*", "Jenkinsfile", "azure-pipelines.yml", ".travis.yml",
	".buildkite/*", "bitbucket-pipelines.yml", ".drone.yml", ".woodpecker.yml",
	".woodpecker/*", ".forgejo/workflows/*", ".gitea/workflows/*", "action.yml", "action.yaml",
}

// credentialNames are files that hold credentials wherever they sit.
var credentialNames = []string{
	".env", ".env.*", "*.pem", "*.key", "*.p12", "*.pfx", "id_rsa*", "id_ed25519*", "id_ecdsa*",
	".npmrc", ".pypirc", ".netrc", "_netrc", ".git-credentials", "credentials", "credentials.json",
	"*.keystore", "*.jks", "secrets.yml", "secrets.yaml", ".dockercfg",
}

// SensitivePath says what a path is when a tainted turn must not change
// it unasked: "CI configuration" or "a credential file", "" otherwise.
// rel is relative to the repository root.
func SensitivePath(rel string) string {
	rel = strings.TrimPrefix(filepath.ToSlash(filepath.Clean(rel)), "./")
	for _, pat := range ciPatterns {
		if strings.HasSuffix(pat, "/*") {
			if dir := strings.TrimSuffix(pat, "*"); strings.HasPrefix(rel, dir) {
				return "CI configuration"
			}
			continue
		}
		if rel == pat {
			return "CI configuration"
		}
	}
	base := path.Base(rel)
	for _, pat := range credentialNames {
		if ok, _ := path.Match(pat, base); ok {
			if base == ".env.example" || base == ".env.sample" || base == ".env.template" {
				continue // the checked-in shape, never the values
			}
			return "a credential file"
		}
	}
	return ""
}
