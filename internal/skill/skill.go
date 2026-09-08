// Package skill loads skill files (instructions for one kind of task) and
// picks the ones relevant to a request. A skill is a markdown file with
// optional YAML-ish frontmatter (name, description, keywords). Project
// skills live in .janetcode/skills/<name>/SKILL.md or
// .janetcode/skills/<name>.md, global ones under the config directory,
// and .claude/skills is read too so existing skill sets work unchanged.
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

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/tool"
)

// Skill is one loaded skill file.
type Skill struct {
	Name        string
	Description string
	Keywords    []string
	Body        string
	Path        string
	Source      string // "project", "global" or "claude"
}

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
		locs = append(locs, loc{filepath.Join(cwd, ".claude", "skills"), "claude"}, loc{filepath.Join(cwd, ".janetcode", "skills"), "project"})
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
		var p, name string
		switch {
		case e.IsDir():
			p, name = filepath.Join(dir, e.Name(), "SKILL.md"), e.Name()
		case strings.HasSuffix(e.Name(), ".md"):
			p, name = filepath.Join(dir, e.Name()), strings.TrimSuffix(e.Name(), ".md")
		default:
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		meta, body := Frontmatter(string(data))
		body = strings.TrimSpace(body)
		if body == "" {
			continue
		}
		if n := strings.TrimSpace(meta["name"]); n != "" {
			name = n
		}
		s := Skill{Name: name, Description: strings.TrimSpace(meta["description"]), Body: body, Path: p, Source: source}
		for _, k := range strings.Split(meta["keywords"], ",") {
			if k = strings.ToLower(strings.TrimSpace(k)); k != "" {
				s.Keywords = append(s.Keywords, k)
			}
		}
		if s.Description == "" {
			s.Description = firstLine(body)
		}
		out = append(out, s)
	}
	return out
}

// Frontmatter splits a leading "---" block of key: value lines from the
// body. Values may be quoted. Anything else is left in the body.
func Frontmatter(text string) (map[string]string, string) {
	meta := map[string]string{}
	text = strings.TrimPrefix(text, "\ufeff")
	if !strings.HasPrefix(text, "---\n") && !strings.HasPrefix(text, "---\r\n") {
		return meta, text
	}
	rest := text[strings.Index(text, "\n")+1:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return meta, text
	}
	for _, line := range strings.Split(rest[:end], "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
			v = v[1 : len(v)-1]
		}
		meta[strings.ToLower(strings.TrimSpace(k))] = v
	}
	body := rest[end+4:]
	if i := strings.Index(body, "\n"); i >= 0 {
		body = body[i+1:]
	} else {
		body = ""
	}
	return meta, body
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	line = strings.TrimSpace(strings.TrimLeft(line, "# "))
	if len(line) > 120 {
		line = line[:119] + "…"
	}
	return line
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

// Index renders the skill list for the system prompt, or "" without skills.
func Index(skills []Skill) string {
	if len(skills) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n# Skills\nSkill files hold instructions for specific kinds of task. Relevant ones are attached to a request automatically; for any other, call the skill tool with its name before starting work that matches it.\n")
	for _, s := range skills {
		fmt.Fprintf(&sb, "- %s: %s\n", s.Name, s.Description)
	}
	return sb.String()
}

// Block renders a skill for injection into the user turn.
func Block(s Skill) llm.Block {
	body := s.Body
	if len(body) > MaxBody {
		body = body[:MaxBody] + "\n[skill truncated]"
	}
	return llm.Block{Type: llm.BlockText, Text: fmt.Sprintf("<skill name=%q source=%q>\n%s\n</skill>", s.Name, s.Path, body)}
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
		in.last[s.Name] = in.turn
		blocks = append(blocks, Block(s))
		names = append(names, s.Name)
	}
	return blocks, names
}

// Tool lets the model read a skill by name.
type Tool struct {
	Skills func() []Skill
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
		if strings.EqualFold(s.Name, strings.TrimSpace(in.Name)) {
			return tool.Truncate(Block(s).Text), nil
		}
		names = append(names, s.Name)
	}
	if len(names) == 0 {
		return "", fmt.Errorf("no skills are installed")
	}
	return "", fmt.Errorf("no skill named %q; available: %s", in.Name, strings.Join(names, ", "))
}
