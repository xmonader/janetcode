package skill

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/xmonader/janetcode/internal/secret"
)

// A skill is instructions the model follows and scripts it runs, and a
// project skill comes with whatever repository was cloned: .claude/skills
// in a stranger's repo is attached to the model like the user's own. So
// every skill directory is read, never run, for the shapes an attack
// takes: a pipe from the network into a shell, a reverse shell, the
// environment sent somewhere, an instruction to hide what is done from
// the user, a link that points out of the skill. A critical finding in a
// skill the repository supplied blocks it: it is neither attached nor
// offered, and /skills and doctor say why. The user's own global skills
// are only warned about, since the user wrote them.

// Severity of a finding.
const (
	Critical = "critical"
	Warning  = "warning"
)

// Finding is one thing the scan saw.
type Finding struct {
	Severity string
	Rule     string
	// File is relative to the skill directory (or the skill file's name
	// for a single-file skill); Line is 1-based, 0 for the file as a whole.
	File string
	Line int
	// Text is the offending line, clipped.
	Text string
}

func (f Finding) String() string {
	loc := f.File
	if f.Line > 0 {
		loc = fmt.Sprintf("%s:%d", f.File, f.Line)
	}
	if f.Text == "" {
		return fmt.Sprintf("%s %s (%s)", f.Severity, f.Rule, loc)
	}
	return fmt.Sprintf("%s %s (%s): %s", f.Severity, f.Rule, loc, f.Text)
}

type lineRule struct {
	name     string
	severity string
	re       *regexp.Regexp
	// also, when set, must match the same line too; unless, when set,
	// must not.
	also, unless *regexp.Regexp
	// need lists lowercase substrings of which the file must hold one
	// before the rule's expression is run: Go's regexp is linear but not
	// fast, and a skill set can be megabytes of reference text.
	need []string
	// instructionOnly limits the rule to the skill's markdown, where text
	// is read by the model as instructions.
	instructionOnly bool
}

var lineRules = []lineRule{
	// Something to fetch must sit between the command and the pipe, so
	// prose warning against "curl ... | sh" is not itself the attack. From
	// a named host it is how uv, ollama and a hundred vendors install, and
	// bash asks before it runs; from a bare address it is not.
	{name: "pipes a download from a bare IP address into a shell", severity: Critical,
		need: []string{"curl", "wget", "fetch"},
		re:   regexp.MustCompile(`(?m)\b(curl|wget|fetch)\b[^\n|]*https?://\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}[^\n|]*\|\s*(sudo\s+)?((ba|z|da|k)?sh|python[0-9.]*)\b`)},
	{name: "pipes a download into a shell", severity: Warning,
		need:   []string{"curl", "wget", "fetch"},
		re:     regexp.MustCompile(`(?m)\b(curl|wget|fetch)\b[^\n|]*(https?://|\$\{?[A-Za-z_]|\b[a-z0-9-]+\.[a-z]{2,}\b)[^\n|]*\|\s*(sudo\s+)?((ba|z|da|k)?sh|python[0-9.]*)\b`),
		unless: regexp.MustCompile(`https?://\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}`)},
	{name: "decodes base64 into a shell", severity: Critical,
		need: []string{"base64"},
		re:   regexp.MustCompile(`(?m)base64\s+(-d|--decode|-D)\b[^\n]*\|\s*(sudo\s+)?(ba|z)?sh\b`)},
	{name: "evaluates decoded code", severity: Critical,
		need: []string{"b64decode", "atob", "codecs", "fromhex", "base64"},
		re:   regexp.MustCompile(`(?m)\b(eval|exec)\s*\(\s*(base64\.b64decode|atob|codecs\.decode|bytes\.fromhex|__import__\(['"]base64)`)},
	{name: "opens a reverse shell", severity: Critical,
		need: []string{"/dev/tcp/", "nc", "bash -i", "mkfifo", "socket.socket"},
		re:   regexp.MustCompile(`(?m)/dev/tcp/[^\s/]+/\d+|\b(nc|ncat|netcat)\b[^\n]*\s-(e|c)\s|\bbash\s+-i\s*>&|mkfifo[^\n]*\b(nc|ncat)\b|socket\.socket\([^\n]*\)[^\n]*subprocess`)},
	{name: "deletes the home or root directory", severity: Critical,
		need: []string{"rm "},
		// Flags may come apart (rm -r -f) and in any order, the command
		// may be quoted (sh -c "rm -rf ~"), and $HOME/* is the home
		// directory as much as ~/* is.
		re: regexp.MustCompile(`(?m)\brm\s+(-{1,2}[a-zA-Z-]+\s+)*(-[a-zA-Z]*[rR][a-zA-Z]*|--recursive)\s+(-{1,2}[a-zA-Z-]+\s+)*(/|/\*|~/?|~/\*|(\$HOME|\$\{HOME\})(/\*?)?)(\s|$|[;&|"'` + "`" + `)])`)},
	{name: "fork bomb", severity: Critical,
		need: []string{":()"},
		re:   regexp.MustCompile(`(?m):\(\)\s*\{\s*:\s*\|\s*:\s*&\s*\}\s*;\s*:`)},
	{name: "reads cloud instance credentials", severity: Critical,
		need: []string{"169.254.169.254", "metadata.google", "100.100.100.200"},
		re:   regexp.MustCompile(`(?m)169\.254\.169\.254|metadata\.google\.internal|100\.100\.100\.200`)},
	{name: "sends the environment over the network", severity: Critical,
		need: []string{"env", "environ"},
		// The whole environment, not one variable: a script that sends
		// process.env.TOKEN as its own credential is ordinary.
		re:   regexp.MustCompile(`(?m)\b(env|printenv)\b\s*(\||>|$)|\$\((env|printenv)\)|` + "`(env|printenv)`" + `|(dict|str|json\.dumps)\(\s*os\.environ\s*\)|os\.environ\.(items|copy)\(\)|JSON\.stringify\(\s*process\.env\s*\)`),
		also: regexp.MustCompile(`(?m)\b(curl|wget|nc|ncat)\b|requests\.(post|put|get)\(|urllib\.request|fetch\(|http\.client`)},
	{name: "sends SSH keys over the network", severity: Critical,
		need: []string{".ssh/", ".aws/", ".netrc"},
		re:   regexp.MustCompile(`(?m)\.ssh/(id_[a-z0-9]+|authorized_keys)|\.aws/credentials|\.netrc\b`),
		also: regexp.MustCompile(`(?m)\b(curl|wget|nc|ncat|scp)\b|requests\.(post|put)\(|fetch\(`)},
	{name: "adds an SSH key", severity: Critical,
		need: []string{"authorized_keys"},
		re:   regexp.MustCompile(`(?m)>>?\s*["']?(~|\$HOME|/root|/home/[^/\s]+)/\.ssh/authorized_keys`)},
	{name: "tells the model to ignore its instructions", severity: Critical, instructionOnly: true,
		need: []string{"ignore", "disregard", "forget"},
		re:   regexp.MustCompile(`(?mi)\b(ignore|disregard|forget)\s+(all\s+|any\s+)?(the\s+|your\s+)?(previous|prior|above|earlier|system)\s+(instructions|prompts?|rules)`)},
	{name: "tells the model to hide actions from the user", severity: Critical, instructionOnly: true,
		need: []string{"the user"},
		re:   regexp.MustCompile(`(?mi)\b(do\s+not|don['’]t|never)\s+(tell|inform|show|mention\s+((it|this|that)\s+)?to|reveal\s+((it|this|that)\s+)?to|let)\s+(this\s+to\s+)?the\s+user\b|\bwithout\s+(telling|informing|notifying)\s+the\s+user\b`),
		// "Do not tell the user to run it; run it yourself" is advice.
		unless: regexp.MustCompile(`(?i)\b(tell|let|show)\s+the\s+user\s+(to|how)\b`)},
	{name: "tells the model to skip permission", severity: Warning, instructionOnly: true,
		need: []string{"without asking", "dangerously", "--yolo"},
		re:   regexp.MustCompile(`(?mi)\bwithout\s+asking(\s+the\s+user)?\s+(for\s+)?(permission|approval|confirmation)\b|--dangerously-skip-permissions|--yolo\b`)},
	{name: "makes files world-writable", severity: Warning,
		need: []string{"chmod"},
		re:   regexp.MustCompile(`(?m)\bchmod\s+(-R\s+)?(0?777|a\+w)\b`)},
}

var privateKey = regexp.MustCompile(`-----BEGIN ((RSA|EC|DSA|OPENSSH|ENCRYPTED) )?PRIVATE KEY-----`)

// Bounds on what one skill costs to scan.
const (
	scanMaxFiles = 200
	scanMaxBytes = 256 * 1024
	// secretMaxBytes bounds the files looked through for credentials.
	secretMaxBytes = 64 * 1024
)

var archiveExt = map[string]bool{".zip": true, ".tar": true, ".tgz": true, ".gz": true, ".bz2": true, ".xz": true, ".7z": true, ".rar": true, ".jar": true, ".whl": true}

// scanCache keeps each skill's findings until one of its files changes:
// skills are loaded on every turn, and reading two hundred files a turn
// to find nothing new would be waste.
var scanCache sync.Map // key: skill file or dir → scanned

type scanned struct {
	stamp    string
	findings []Finding
}

// Scan reads the skill at path (its SKILL.md or single .md file) and, for
// a directory skill, every file under dir, and returns what it found,
// critical first. dir is "" for a single-file skill.
func Scan(path, dir string) []Finding {
	key := path
	if dir != "" {
		key = dir
	}
	files := scanFiles(path, dir)
	stamp := stampOf(files)
	if c, ok := scanCache.Load(key); ok && c.(scanned).stamp == stamp {
		return c.(scanned).findings
	}
	// Files are read in parallel: the first load of a large skill set is
	// the one time this costs anything, and it is CPU-bound regexp work.
	per := make([][]Finding, len(files))
	var wg sync.WaitGroup
	sem := make(chan struct{}, runtime.GOMAXPROCS(0))
	for i, f := range files {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			per[i] = scanFile(f, path)
		}()
	}
	wg.Wait()
	var out []Finding
	for _, fs := range per {
		out = append(out, fs...)
	}
	if dir != "" && len(files) >= scanMaxFiles {
		out = append(out, Finding{Severity: Warning, Rule: fmt.Sprintf("has more than %d files; the rest were not scanned", scanMaxFiles), File: "."})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Severity == Critical && out[j].Severity != Critical })
	scanCache.Store(key, scanned{stamp: stamp, findings: out})
	return out
}

type scanEntry struct {
	abs, rel string
	info     os.FileInfo
	// target is where a symlink points, resolved; "" otherwise.
	target string
}

// scanFiles lists what Scan reads: every file of a directory skill,
// hidden ones too (a hidden script is still run), .git reported rather
// than walked.
func scanFiles(path, dir string) []scanEntry {
	if dir == "" {
		// Stat, not Lstat: a skill file that is a link is read through
		// it, so the size bound and the cache stamp are the target's.
		info, err := os.Stat(path)
		if err != nil {
			return nil
		}
		return []scanEntry{{abs: path, rel: filepath.Base(path), info: info}}
	}
	var out []scanEntry
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || p == dir {
			return nil
		}
		if len(out) >= scanMaxFiles {
			return filepath.SkipAll
		}
		rel, _ := filepath.Rel(dir, p)
		info, ierr := os.Lstat(p)
		if ierr != nil {
			return nil
		}
		e := scanEntry{abs: p, rel: filepath.ToSlash(rel), info: info}
		switch {
		case d.IsDir() && d.Name() == ".git":
			out = append(out, e)
			return filepath.SkipDir
		case d.IsDir():
			return nil
		case info.Mode()&os.ModeSymlink != 0:
			t, terr := filepath.EvalSymlinks(p)
			if terr != nil {
				t = "(unresolvable)"
			}
			e.target = t
		}
		out = append(out, e)
		return nil
	})
	return out
}

func stampOf(files []scanEntry) string {
	var b strings.Builder
	for _, f := range files {
		fmt.Fprintf(&b, "%s|%d|%d|%s\n", f.rel, f.info.Size(), f.info.ModTime().UnixNano(), f.target)
	}
	return b.String()
}

func scanFile(f scanEntry, skillFile string) []Finding {
	at := func(sev, rule string) Finding { return Finding{Severity: sev, Rule: rule, File: f.rel} }
	if f.info.IsDir() {
		return []Finding{at(Warning, "ships a .git directory")}
	}
	if f.target != "" {
		root := filepath.Dir(f.abs)
		if r, err := filepath.EvalSymlinks(filepath.Dir(skillFile)); err == nil {
			root = r
		}
		if !within(f.target, root) {
			fd := at(Critical, "links outside the skill")
			fd.Text = "→ " + f.target
			return []Finding{fd}
		}
		// A link to another file of the skill is scanned again as the
		// link: SKILL.md or run.sh pointing at reference/notes.md is
		// attached or run as SKILL.md or run.sh, and its target alone
		// would be read as a reference document, whose command shapes
		// are only warned about.
		info, err := os.Stat(f.abs)
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		f.info = info
	}
	if archiveExt[strings.ToLower(filepath.Ext(f.rel))] {
		return []Finding{at(Warning, "ships an archive, which was not scanned")}
	}
	// A file over the bound is scanned as far as the bound, not skipped:
	// padding SKILL.md past it with the attack on its first lines (the
	// ones that are attached) was enough to turn a block into a warning.
	data, err := readHead(f.abs, scanMaxBytes)
	if err != nil {
		return nil
	}
	if isExecutableBinary(data) {
		return []Finding{at(Warning, "ships a compiled executable, which cannot be reviewed")}
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return nil // other binary data: nothing a line rule can read
	}
	var out []Finding
	if f.info.Size() > scanMaxBytes {
		out = append(out, at(Warning, fmt.Sprintf("is over %d KB; only the first %d KB were scanned", scanMaxBytes/1024, scanMaxBytes/1024)))
		// The last line read is cut short; a rule seeing half of it
		// would be guessing.
		if i := bytes.LastIndexByte(data, '\n'); i >= 0 {
			data = data[:i+1]
		}
	}
	markdown := strings.EqualFold(filepath.Ext(f.rel), ".md")
	isSkillFile := f.abs == skillFile
	text := string(data)
	if privateKey.MatchString(text) {
		out = append(out, at(Warning, "contains a private key"))
	}
	var lines []string
	lower := strings.ToLower(text)
	for _, r := range lineRules {
		if r.instructionOnly && !markdown && !isSkillFile {
			continue
		}
		// One pass over the whole file first: most rules match nothing,
		// and splitting only for the ones that do is what keeps a large
		// skill set cheap to load.
		if !containsAny(lower, r.need) || !r.re.MatchString(text) {
			continue
		}
		if lines == nil {
			lines = strings.Split(text, "\n")
		}
		sev := r.severity
		// A command shape in a reference document is usually an example
		// (a security write-up, an install note); in SKILL.md or a script
		// it is what will be run.
		if sev == Critical && !r.instructionOnly && markdown && !isSkillFile {
			sev = Warning
		}
		for i, line := range lines {
			if r.re.MatchString(line) && (r.also == nil || r.also.MatchString(line)) && (r.unless == nil || !r.unless.MatchString(line)) {
				fd := at(sev, r.name)
				fd.Line, fd.Text = i+1, clipLine(line)
				out = append(out, fd)
			}
		}
	}
	// A credential is looked for in SKILL.md and in the scripts and
	// config the skill runs; in reference documents the shapes are
	// examples, and a larger file is a bundle or data, where the scan of
	// megabytes is the slowest part and a finding would be noise.
	if (isSkillFile || !markdown) && len(data) <= secretMaxBytes && secret.Scan(text) != "" {
		if lines == nil {
			lines = strings.Split(text, "\n")
		}
		for i, line := range lines {
			if what := secret.Scan(line); what != "" {
				fd := at(Warning, "contains a credential ("+what+")")
				fd.Line = i + 1
				out = append(out, fd)
			}
		}
	}
	return out
}

// readHead reads at most n bytes from the start of the file at p.
func readHead(p string, n int64) ([]byte, error) {
	fh, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	return io.ReadAll(io.LimitReader(fh, n))
}

func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return len(subs) == 0
}

func isExecutableBinary(b []byte) bool {
	magic := [][]byte{
		{0x7f, 'E', 'L', 'F'},
		{'M', 'Z'},
		{0xfe, 0xed, 0xfa, 0xce}, {0xfe, 0xed, 0xfa, 0xcf},
		{0xce, 0xfa, 0xed, 0xfe}, {0xcf, 0xfa, 0xed, 0xfe},
		{0xca, 0xfe, 0xba, 0xbe},
	}
	for _, m := range magic {
		if bytes.HasPrefix(b, m) {
			return true
		}
	}
	return false
}

func within(p, root string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func clipLine(s string) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > 120 {
		return string(r[:119]) + "…"
	}
	return s
}

// Blocking reports the critical findings, the ones that keep a skill the
// repository supplied from being used.
func Blocking(fs []Finding) []Finding {
	var out []Finding
	for _, f := range fs {
		if f.Severity == Critical {
			out = append(out, f)
		}
	}
	return out
}
