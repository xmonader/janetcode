package shell

import (
	"path"
	"regexp"
	"slices"
	"strings"
)

// destructiveRisk grades the commands whose effect is not in the paths on
// the command line: an interpreter one-liner that writes or deletes in its
// own code, xargs feeding a mutator names it from stdin, and the git
// subcommands that discard work or rewrite history. None of them is a
// read, so they ask; the whole line is the user's to read.
func destructiveRisk(toks []string) (Risk, string) {
	if len(toks) == 0 {
		return RiskOrdinary, ""
	}
	name := path.Base(toks[0])
	switch {
	case name == "xargs":
		if inner := wrapped(toks); len(inner) > 0 && xargsMutators[path.Base(inner[0])] {
			return RiskDestructive, "xargs " + path.Base(inner[0]) + " changes files it is given on stdin"
		}
	case name == "git":
		if why := destructiveGit(toks[1:]); why != "" {
			return RiskDestructive, why
		}
	case interpreterRe.MatchString(name):
		if interpreterWrites(toks[1:]) {
			return RiskDestructive, name + " code that writes or deletes files"
		}
	}
	return RiskOrdinary, ""
}

var xargsMutators = map[string]bool{
	"rm": true, "rmdir": true, "mv": true, "cp": true, "chmod": true, "chown": true, "chgrp": true,
	"truncate": true, "tee": true, "ln": true, "install": true, "rsync": true, "shred": true, "unlink": true,
}

var interpreterRe = regexp.MustCompile(`^(python[0-9.]*|pypy[0-9]*|node|nodejs|deno|bun|perl|ruby|php)$`)

// evalFlags are the flags that take the program as an argument.
var evalFlags = []string{"-c", "-e", "-E", "-r", "-R", "-pe", "-ne", "-lne", "-lpe", "-pi", "-i", "-p", "-n", "--eval", "--print", "--exec"}

// writeCodeRe matches the calls an inline program uses to write, delete or
// run something: it is a list of spellings, not a parser, and a program
// that avoids all of them is for the prompt on the line to catch.
var writeCodeRe = regexp.MustCompile(`(?i)` +
	`open\s*\([^)]*['"][^'"]*[wax+][^'"]*['"]` + // python open(f, 'w')
	`|\.(write_text|write_bytes|writelines|unlink|rmdir|touch|mkdir|rename|truncate)\s*\(` +
	`|\b(os|shutil|fs|Path|File|FileUtils|Deno)\.(remove|unlink|rmdir|rm|rename|replace|chmod|chown|truncate|symlink|link|mkdir|move|copy|write|append|create|delete|open)\w*\s*\(` + // os.remove, shutil.move, fs.rmSync ...
	`|\b(rmtree|copytree|copyfile|copy2|makedirs|removedirs)\s*\(` +
	`|\b(system|popen|execl|execv|execvp|spawn|spawnSync|execSync|check_call|check_output|subprocess|child_process)\b` +
	`|\b(writeFile|writeFileSync|appendFile|appendFileSync|rmSync|unlinkSync|createWriteStream|file_put_contents|fwrite|fopen|unlink)\b`)

func interpreterWrites(args []string) bool {
	for i, a := range args {
		if a == "--" || !strings.HasPrefix(a, "-") {
			return false // a script file or a module: not an inline program
		}
		if slices.Contains(evalFlags, a) || strings.HasPrefix(a, "--eval=") {
			return writeCodeRe.MatchString(strings.Join(args[i:], " "))
		}
	}
	return false
}

// destructiveGit names what a git command discards or rewrites, or "" for
// one that does not.
func destructiveGit(args []string) string {
	i := 0
	for i < len(args) && strings.HasPrefix(args[i], "-") {
		switch args[i] {
		case "-C", "-c", "--git-dir", "--work-tree", "--namespace", "--exec-path", "--config-env":
			i++
		}
		i++
	}
	if i >= len(args) {
		return ""
	}
	sub, rest := args[i], args[i+1:]
	has := func(flags ...string) bool {
		for _, a := range rest {
			if a == "--" {
				return false
			}
			for _, f := range flags {
				if a == f || strings.HasPrefix(a, f+"=") {
					return true
				}
				// bundled short flags: -fdx, -xfd
				if len(f) == 2 && len(a) > 2 && a[0] == '-' && a[1] != '-' && strings.Contains(a[1:], f[1:]) {
					return true
				}
			}
		}
		return false
	}
	switch sub {
	case "push":
		if has("--force", "-f", "--force-with-lease", "--force-if-includes", "--delete", "-d", "--mirror", "--prune") ||
			slices.ContainsFunc(rest, func(a string) bool { return strings.HasPrefix(a, "+") || strings.HasPrefix(a, ":") && len(a) > 1 }) {
			return "git push that overwrites or deletes remote history"
		}
	case "reset":
		if has("--hard", "--merge", "--keep") {
			return "git reset --hard discards uncommitted work"
		}
	case "clean":
		if has("-f", "--force") {
			return "git clean deletes untracked files"
		}
	case "checkout", "switch":
		if has("-f", "--force", "-B", "-C", "--discard-changes") || slices.Contains(rest, "--") && sub == "checkout" {
			return "git " + sub + " discards uncommitted changes"
		}
	case "restore":
		if !has("--staged", "-S") || has("--worktree", "-W") {
			return "git restore discards uncommitted changes"
		}
	case "branch":
		if has("-D", "--delete", "-M", "--force") && !has("--list") {
			return "git branch deletes or moves a branch"
		}
	case "tag":
		if has("-d", "--delete", "-f", "--force") {
			return "git tag deletes or moves a tag"
		}
	case "stash":
		if len(rest) > 0 && (rest[0] == "drop" || rest[0] == "clear") {
			return "git stash " + rest[0] + " discards stashed work"
		}
	case "rebase", "filter-branch", "filter-repo", "update-ref", "gc", "prune", "reflog":
		if sub == "gc" && !has("--prune") {
			return ""
		}
		if sub == "reflog" && (len(rest) == 0 || rest[0] != "expire" && rest[0] != "delete") {
			return ""
		}
		if sub == "update-ref" && !has("-d") {
			return ""
		}
		return "git " + sub + " rewrites or discards history"
	}
	return ""
}
