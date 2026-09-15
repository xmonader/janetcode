// Package skill loads skill files (instructions for one kind of task) and
// picks the ones relevant to a request. A skill is a markdown file with
// optional YAML-ish frontmatter (name, description, keywords). Project
// skills live in .janetcode/skills/<name>/SKILL.md or
// .janetcode/skills/<name>.md, global ones under the config directory,
// .agents/skills and .claude/skills so existing skill sets work unchanged.
package skill

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/md"
	"github.com/xmonader/janetcode/internal/memory"
	"github.com/xmonader/janetcode/internal/perm"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// Skill is one loaded skill file.
type Skill struct {
	Name        string
	Description string
	Keywords    []string
	Body        string
	Path        string
	Source      string // "project", "agents", "global", "claude" or "builtin"
	// Origin is the frontmatter source/origin of a generated skill
	// ("auto-skill", "learned"), distinct from Source which is where it
	// was loaded from.
	Origin string
	// Files are the supporting files of a directory skill (scripts,
	// references), relative to Dir; the model reads or runs them by path.
	Dir   string
	Files []string
	// UserInvocable allows "/name args" in the TUI (default true).
	UserInvocable bool
	// ModelInvocable allows automatic attachment and the skill tool
	// (default true; frontmatter disable-model-invocation: true turns it off).
	ModelInvocable bool
	// Paths are globs that attach the skill when a tool touches a matching
	// file, the same way project rules work. Empty means text-match only.
	Paths []string
}

// maxFiles bounds the supporting files listed per skill.
const maxFiles = 40

// MaxSelected bounds how many skills one request can pull in.
const MaxSelected = 5

// MaxBody bounds the text injected per skill.
const MaxBody = 16 * 1024

// Load reads every skill visible from cwd; a project skill shadows a global
// one of the same name. Pass "" for a location to skip it.
func Load(cwd, configDir string) []Skill {
	byName := map[string]Skill{}
	type loc struct{ dir, source string }
	var locs []loc
	if configDir != "" {
		locs = append(locs, loc{filepath.Join(configDir, "skills"), "global"})
	}
	if cwd != "" {
		locs = append(locs,
			loc{filepath.Join(cwd, ".claude", "skills"), "claude"},
			loc{filepath.Join(cwd, ".agents", "skills"), "agents"},
			loc{filepath.Join(cwd, ".janetcode", "skills"), "project"},
		)
	}
	for _, l := range locs {
		for _, s := range readDir(l.dir, l.source) {
			byName[s.Name] = s
		}
	}
	out := make([]Skill, 0, len(byName))
	for _, s := range byName {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func readDir(dir, source string) []Skill {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []Skill
	for _, e := range entries {
		var p, name, sdir string
		switch {
		case e.IsDir():
			sdir = filepath.Join(dir, e.Name())
			p, name = filepath.Join(sdir, "SKILL.md"), e.Name()
		case strings.HasSuffix(e.Name(), ".md"):
			p, name = filepath.Join(dir, e.Name()), strings.TrimSuffix(e.Name(), ".md")
		default:
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		meta, body := md.Parse(string(data))
		body = strings.TrimSpace(body)
		if body == "" {
			continue
		}
		if n := strings.TrimSpace(meta["name"]); n != "" {
			name = n
		}
		s := Skill{Name: name, Description: strings.TrimSpace(meta["description"]), Body: body, Path: p, Source: source, Dir: sdir,
			Origin:         skillOrigin(name, meta),
			UserInvocable:  !isTrue(meta["disable-user-invocation"]) && meta["user-invocable"] != "false",
			ModelInvocable: !isTrue(meta["disable-model-invocation"]),
			Paths:          md.List(meta["paths"])}
		for _, k := range md.List(meta["keywords"]) {
			s.Keywords = append(s.Keywords, strings.ToLower(k))
		}
		for _, k := range md.List(meta["tags"]) {
			s.Keywords = append(s.Keywords, strings.ToLower(k))
		}
		if s.Description == "" {
			s.Description = textfmt.HeadingLine(body, 120)
		}
		if sdir != "" {
			s.Files = supportingFiles(sdir)
		}
		out = append(out, s)
	}
	return out
}

func isTrue(v string) bool { return v == "true" || v == "yes" || v == "1" }

func skillOrigin(name string, meta map[string]string) string {
	o := strings.ToLower(strings.TrimSpace(meta["origin"]))
	if o == "" {
		o = strings.ToLower(strings.TrimSpace(meta["source"]))
	}
	switch o {
	case "project", "global", "claude", "agents", "builtin":
		o = ""
	}
	if o == "" && (strings.HasPrefix(name, "auto-skill-") || strings.HasPrefix(name, "learned-")) {
		return "auto-skill"
	}
	return o
}

// supportingFiles lists the files under a skill directory besides
// SKILL.md, relative to it, hidden files skipped.
func supportingFiles(dir string) []string {
	var out []string
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || len(out) >= maxFiles {
			return nil
		}
		name := d.Name()
		if strings.HasPrefix(name, ".") && p != dir {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() || name == "SKILL.md" {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		out = append(out, rel)
		return nil
	})
	return out
}

// Expand fills a skill body for a "/name args" invocation: $ARGUMENTS is
// replaced, or the arguments are appended.
func (s Skill) Expand(args string) string {
	args = strings.TrimSpace(args)
	body := s.Block().Text
	if strings.Contains(body, "$ARGUMENTS") {
		return strings.ReplaceAll(body, "$ARGUMENTS", args)
	}
	if args == "" {
		return body
	}
	return body + "\n\n" + args
}

var wordRe = regexp.MustCompile(`[a-z0-9][a-z0-9_.+-]{2,}`)

func words(s string) []string {
	ws := wordRe.FindAllString(strings.ToLower(s), -1)
	for i, w := range ws {
		ws[i] = stem(w)
	}
	return ws
}

// stem folds plurals so "migration" and "migrations" count as one word.
func stem(w string) string {
	switch {
	case strings.HasSuffix(w, "ies") && len(w) > 4:
		return w[:len(w)-3] + "y"
	case strings.HasSuffix(w, "ss"):
		return w
	case strings.HasSuffix(w, "s") && len(w) > 3:
		return w[:len(w)-1]
	}
	return w
}

// Select ranks skills against the request text lexically and returns the
// best matches, at most max. A skill needs a clear signal: its name or a
// keyword in the text, or several words of its description.
func Select(skills []Skill, text string, max int) []Skill {
	if max <= 0 {
		max = MaxSelected
	}
	ws := words(text)
	if len(ws) == 0 {
		return nil
	}
	have := map[string]bool{}
	for _, w := range ws {
		have[w] = true
	}
	lower := strings.ToLower(text)
	type scored struct {
		s     Skill
		score int
	}
	var hits []scored
	for _, s := range skills {
		if !s.ModelInvocable {
			continue
		}
		score := 0
		name := strings.ToLower(s.Name)
		if strings.Contains(lower, name) || strings.Contains(lower, strings.ReplaceAll(name, "-", " ")) {
			score += 10
		}
		for _, k := range s.Keywords {
			if strings.Contains(lower, k) {
				score += 5
			}
		}
		seen := map[string]bool{}
		for _, w := range words(s.Description) {
			if have[w] && !seen[w] && !stop[w] {
				seen[w] = true
				score++
			}
		}
		for _, w := range words(headings(s.Body)) {
			if have[w] && !seen[w] && !stop[w] {
				seen[w] = true
				score++
			}
		}
		if score >= 3 {
			hits = append(hits, scored{s, score})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	if len(hits) > max {
		hits = hits[:max]
	}
	out := make([]Skill, len(hits))
	for i, h := range hits {
		out[i] = h.s
	}
	return out
}

// stop words carry no signal on their own.
var stop = map[string]bool{
	"the": true, "and": true, "for": true, "with": true, "this": true, "that": true, "from": true,
	"you": true, "are": true, "use": true, "when": true, "how": true, "what": true, "into": true,
	"file": true, "files": true, "code": true, "project": true, "run": true, "make": true, "new": true,
	"add": true, "all": true, "any": true, "can": true, "not": true, "but": true, "its": true,
}

// headings returns the markdown headings of a body, which name what the
// skill is about better than its prose.
func headings(body string) string {
	var sb strings.Builder
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "#") {
			sb.WriteString(strings.TrimLeft(line, "# ") + " ")
		}
	}
	return sb.String()
}

// IndexWithin renders the skill list for the system prompt, or "" without
// model-invocable skills, within a byte budget (memory.IndexBudget when
// budget is not positive). The list had none: a config
// directory with a hundred skills in it put all hundred lines in every
// request, and the skill tool can find the rest by name, which is what
// the paragraph above already tells the model to do.
func IndexWithin(skills []Skill, budget int) string {
	if budget <= 0 {
		budget = memory.IndexBudget
	}
	const head = "\n# Skills\nSkill files hold instructions for specific kinds of task. Relevant ones are attached to a request automatically; for any other, call the skill tool with its name before starting work that matches it.\n"
	var sb strings.Builder
	sb.WriteString(head) // the heading counts against the budget too
	shown, total := 0, 0
	for _, s := range skills {
		if !s.ModelInvocable {
			continue
		}
		// Builtin skills are reachable by name and by Select; listing
		// them here would put a line in every empty project's prefix.
		if s.Source == "builtin" {
			continue
		}
		total++
		line := fmt.Sprintf("- %s: %s\n", s.Name, s.Description)
		if sb.Len()+len(line) > budget {
			continue
		}
		sb.WriteString(line)
		shown++
	}
	if shown == 0 {
		return ""
	}
	if shown < total {
		fmt.Fprintf(&sb, "- (%d more; the skill tool finds them by name)\n", total-shown)
	}
	return sb.String()
}

// Block renders a skill for injection into the user turn, listing its
// supporting files so the model can read or run them.
func (s Skill) Block() llm.Block {
	body := s.Body
	if len(body) > MaxBody {
		body = body[:MaxBody] + "\n[skill truncated]"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "<skill name=%q source=%q>\n%s\n", s.Name, s.Path, body)
	if len(s.Files) > 0 {
		fmt.Fprintf(&sb, "\nFiles shipped with this skill (under %s):\n", s.Dir)
		for _, f := range s.Files {
			fmt.Fprintf(&sb, "- %s\n", filepath.Join(s.Dir, f))
		}
	}
	sb.WriteString("</skill>")
	return llm.Block{Type: llm.BlockText, Text: sb.String()}
}

// Injector attaches matching skills to user turns and avoids re-attaching
// one that is still fresh in the context.
type Injector struct {
	Skills func() []Skill
	// Max is the most skills per turn; zero means MaxSelected.
	Max int

	mu   sync.Mutex
	turn int
	last map[string]int // skill name -> turn it was attached
	used map[string]int // skill name -> times its text was sent
}

// Stat is what one skill did this session: how often its text was sent,
// when that last happened, and whether it is still in the context.
type Stat struct {
	Used     int
	LastTurn int
	Fresh    bool
}

// Stats reports the per-skill record, keyed by skill name. A name that
// is absent was never sent.
func (in *Injector) Stats() map[string]Stat {
	out := map[string]Stat{}
	if in == nil {
		return out
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	for name, n := range in.used {
		out[name] = Stat{Used: n, LastTurn: in.last[name], Fresh: in.turn-in.last[name] < freshTurns}
	}
	return out
}

// mark records that a skill's text went into the context. The caller
// holds the lock.
func (in *Injector) mark(name string) {
	if in.last == nil {
		in.last = map[string]int{}
	}
	if in.used == nil {
		in.used = map[string]int{}
	}
	in.last[name] = in.turn
	in.used[name]++
}

// MarkUsed records a skill the model pulled in itself, with the skill
// tool, rather than one the injector matched. It is the same cost and
// the same freshness, so it counts the same way.
func (in *Injector) MarkUsed(name string) {
	if in == nil {
		return
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	in.mark(name)
}

// freshTurns is how many turns an attached skill stays in effect before
// it is attached again.
const freshTurns = 6

// Prepare appends skill blocks to a user turn's blocks. It returns the
// names it attached.
func (in *Injector) Prepare(blocks []llm.Block) ([]llm.Block, []string) {
	if in == nil || in.Skills == nil {
		return blocks, nil
	}
	var text string
	for _, b := range blocks {
		if b.Type == llm.BlockText {
			text += b.Text + "\n"
		}
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	in.turn++
	if in.last == nil {
		in.last = map[string]int{}
	}
	var names []string
	for _, s := range Select(in.Skills(), text, in.Max) {
		if t, ok := in.last[s.Name]; ok && in.turn-t < freshTurns {
			continue
		}
		in.mark(s.Name)
		b := s.Block()
		b.Synthetic = true
		blocks = append(blocks, b)
		names = append(names, s.Name)
	}
	return blocks, names
}

// Matches reports whether a path a tool touched brings this skill in.
// A glob without a slash is matched against the base name too, so
// "*.sql" covers db/migrations/001.sql.
func (s Skill) Matches(path string) bool {
	if len(s.Paths) == 0 {
		return false
	}
	path = strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(path)), "./")
	if path == "" {
		return false
	}
	base := path
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		base = path[i+1:]
	}
	for _, g := range s.Paths {
		g = strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(g)), "./")
		if g == "" {
			continue
		}
		if perm.Match(g, path) {
			return true
		}
		if !strings.Contains(g, "/") && perm.Match(g, base) {
			return true
		}
	}
	return false
}

// ForPaths returns the text of every path-gated skill these paths bring
// in that has not been sent yet this session.
func (in *Injector) ForPaths(paths []string) []string {
	if in == nil || in.Skills == nil || len(paths) == 0 {
		return nil
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.last == nil {
		in.last = map[string]int{}
	}
	var out []string
	for _, s := range in.Skills() {
		if !s.ModelInvocable || len(s.Paths) == 0 {
			continue
		}
		if t, ok := in.last[s.Name]; ok && in.turn-t < freshTurns {
			continue
		}
		hit := false
		for _, p := range paths {
			if s.Matches(p) {
				hit = true
				break
			}
		}
		if !hit {
			continue
		}
		in.mark(s.Name)
		out = append(out, fmt.Sprintf("[skill: %s — applies to %s]\n%s", s.Name, strings.Join(s.Paths, ", "), s.Block().Text))
	}
	return out
}

// Tool lets the model read a skill by name.
type Tool struct {
	Skills func() []Skill
	// Used, when set, is told the name of a skill the model read, so the
	// skills report can say it earned its place.
	Used func(name string)
}

func (*Tool) Name() string { return "skill" }
func (*Tool) Description() string {
	return "Read a skill file by name: instructions for a specific kind of task, listed under Skills in the system prompt. Call it before starting work the skill covers."
}
func (*Tool) Mutating() bool { return false }
func (*Tool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"name":{"type":"string","description":"Skill name as listed"}},"required":["name"],"additionalProperties":false}`)
}

func (t *Tool) Run(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("bad input: %w", err)
	}
	var names []string
	for _, s := range t.Skills() {
		if !s.ModelInvocable {
			continue
		}
		if strings.EqualFold(s.Name, strings.TrimSpace(in.Name)) {
			if t.Used != nil {
				t.Used(s.Name)
			}
			return textfmt.Truncate(s.Block().Text), nil
		}
		names = append(names, s.Name)
	}
	if len(names) == 0 {
		return "", fmt.Errorf("no skills are installed")
	}
	return "", fmt.Errorf("no skill named %q; available: %s", in.Name, strings.Join(names, ", "))
}

// TurnPrep combines skill attachment and memory recall into one Prepare
// function for the agent: matching skills and the most relevant memory
// entries are appended to the user's turn, and a note names them.
func TurnPrep(in *Injector, mem *memory.Store) func([]llm.Block) ([]llm.Block, string) {
	return func(blocks []llm.Block) ([]llm.Block, string) {
		var text string
		for _, b := range blocks {
			if b.Type == llm.BlockText {
				text += b.Text + "\n"
			}
		}
		var notes []string
		blocks, names := in.Prepare(blocks)
		if len(names) > 0 {
			notes = append(notes, "skills attached: "+strings.Join(names, ", "))
		}
		if mem != nil {
			rctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			hits := mem.RecallCtx(rctx, text, memory.MaxRecall)
			cancel()
			var used []memory.Entry
			size := 0
			for _, e := range hits {
				r := memory.Render(e)
				if size+len(r) > memory.RecallBudget {
					break
				}
				size += len(r)
				blocks = append(blocks, llm.Block{Type: llm.BlockText, Text: r, Synthetic: true})
				used = append(used, e)
			}
			if len(used) > 0 {
				mem.Touch(used...)
				var ns []string
				for _, e := range used {
					ns = append(ns, e.Name)
				}
				notes = append(notes, "memory recalled: "+strings.Join(ns, ", "))
			}
		}
		return blocks, strings.Join(notes, " · ")
	}
}
