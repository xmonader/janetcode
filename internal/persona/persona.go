// Package persona holds subagent personas: a behavioural overlay (tone,
// output format, a contract of inputs and outputs) layered onto a
// subagent's prompt. It is separate from an agent profile on purpose. A
// profile says what a subagent is -- which tools, which model, what job --
// and a persona says how it behaves, so "concise" or "adversarial
// reviewer" combines with any profile instead of being copied into each.
//
// Personas come from the config file's "personas" object, from
// .janetcode/personas/ in a trusted project and from personas/ under the
// config directory. In a directory a persona is name.json (the same shape
// as the config entry) or name.md (frontmatter plus the instructions as the
// body). A project persona shadows a global one, which shadows the
// config's. A persona asked for and not found is an error: the subagent
// does not start without the behaviour the caller asked for.
package persona

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/md"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// IOField is one declared input or output of a persona's contract.
type IOField struct {
	Name        string `json:"name"`
	Type        string `json:"type,omitempty"` // "file", "text", ...
	Required    bool   `json:"required,omitempty"`
	Description string `json:"description"`
}

// Persona is one named overlay.
type Persona struct {
	Name         string    `json:"-"`
	Description  string    `json:"description,omitempty"`
	Tone         string    `json:"tone,omitempty"`
	OutputFormat string    `json:"output_format,omitempty"`
	Instructions string    `json:"instructions,omitempty"`
	Inputs       []IOField `json:"inputs,omitempty"`
	Outputs      []IOField `json:"outputs,omitempty"`
	// Model and Effort apply when neither the task call nor the agent
	// profile names one.
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`

	Source string `json:"-"` // "config", "global" or "project"
	Path   string `json:"-"`
}

const (
	maxPersonaBytes = 8 * 1024
	maxPersonas     = 64
)

// Validate says what is wrong with a persona, if anything.
func (p Persona) Validate() error {
	if strings.TrimSpace(p.Name) == "" || strings.ContainsAny(p.Name, " \t\n/\\") {
		return fmt.Errorf("persona name %q must be one word", p.Name)
	}
	if p.Body() == "" && len(p.Inputs) == 0 && len(p.Outputs) == 0 {
		return fmt.Errorf("persona %q has no instructions, tone, output_format or contract", p.Name)
	}
	if !llm.ValidEffort(p.Effort) {
		return fmt.Errorf("persona %q: effort wants one of %s", p.Name, llm.EffortList())
	}
	return nil
}

// Body is the behavioural text: tone, output format, then instructions.
func (p Persona) Body() string {
	var parts []string
	if t := strings.TrimSpace(p.Tone); t != "" {
		parts = append(parts, "Tone: "+t)
	}
	if f := strings.TrimSpace(p.OutputFormat); f != "" {
		parts = append(parts, "Output format: "+f)
	}
	if i := strings.TrimSpace(p.Instructions); i != "" {
		parts = append(parts, i)
	}
	return strings.Join(parts, "\n\n")
}

// Block is what is added to the subagent's system prompt.
func (p Persona) Block() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "<persona name=%q>\n", p.Name)
	if b := p.Body(); b != "" {
		sb.WriteString(b + "\n")
	}
	contract := func(title string, fs []IOField) {
		if len(fs) == 0 {
			return
		}
		sb.WriteString("\n" + title + ":\n")
		for _, f := range fs {
			req := ""
			if f.Required {
				req = ", required"
			}
			typ := f.Type
			if typ == "" {
				typ = "text"
			}
			fmt.Fprintf(&sb, "- %s (%s%s): %s\n", f.Name, typ, req, f.Description)
		}
	}
	contract("Inputs you expect from the brief", p.Inputs)
	contract("Outputs you must produce", p.Outputs)
	sb.WriteString("</persona>")
	return sb.String()
}

// Summary is the one line a listing shows.
func (p Persona) Summary() string {
	if d := strings.TrimSpace(p.Description); d != "" {
		return d
	}
	return textfmt.HeadingLine(p.Body(), 0)
}

// Load reads every persona visible from cwd. cfg is the config file's
// "personas" object; cwd is "" when the project is not trusted. Files that
// do not parse are skipped and named in the second result.
func Load(cfg map[string]Persona, cwd, configDir string) ([]Persona, []string) {
	byName := map[string]Persona{}
	var bad []string
	for name, p := range cfg {
		p.Name, p.Source = name, "config"
		if err := p.Validate(); err != nil {
			bad = append(bad, "config: "+err.Error())
			continue
		}
		byName[name] = p
	}
	type loc struct{ dir, source string }
	var locs []loc
	if configDir != "" {
		locs = append(locs, loc{filepath.Join(configDir, "personas"), "global"})
	}
	if cwd != "" {
		locs = append(locs, loc{filepath.Join(cwd, ".janetcode", "personas"), "project"})
	}
	for _, l := range locs {
		entries, err := os.ReadDir(l.dir)
		if err != nil {
			continue
		}
		n := 0
		for _, e := range entries {
			ext := filepath.Ext(e.Name())
			if e.IsDir() || (ext != ".json" && ext != ".md") || n >= maxPersonas {
				continue
			}
			n++
			p, err := readFile(filepath.Join(l.dir, e.Name()))
			if err != nil {
				bad = append(bad, err.Error())
				continue
			}
			p.Source = l.source
			if err := p.Validate(); err != nil {
				bad = append(bad, filepath.Join(l.dir, e.Name())+": "+err.Error())
				continue
			}
			byName[p.Name] = p
		}
	}
	out := make([]Persona, 0, len(byName))
	for _, p := range byName {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	sort.Strings(bad)
	return out, bad
}

func readFile(path string) (Persona, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Persona{}, err
	}
	if len(data) > maxPersonaBytes*4 {
		return Persona{}, fmt.Errorf("%s: over %d KB", path, maxPersonaBytes*4>>10)
	}
	base := filepath.Base(path)
	p := Persona{Name: strings.TrimSuffix(base, filepath.Ext(base)), Path: path}
	if filepath.Ext(path) == ".json" {
		if err := json.Unmarshal(data, &p); err != nil {
			return Persona{}, fmt.Errorf("%s: %w", path, err)
		}
	} else {
		meta, body := md.Parse(string(data))
		p.Description = meta["description"]
		p.Tone = meta["tone"]
		p.OutputFormat = meta["output_format"]
		p.Model = meta["model"]
		p.Effort = meta["effort"]
		p.Instructions = body
	}
	p.Instructions = textfmt.CutBytes(strings.TrimSpace(p.Instructions), maxPersonaBytes)
	return p, nil
}

// Find looks a persona up by name, case-insensitively. The error is the
// one a task call returns for a persona that does not exist.
func Find(ps []Persona, name string) (Persona, error) {
	for _, p := range ps {
		if strings.EqualFold(p.Name, name) {
			return p, nil
		}
	}
	names := make([]string, 0, len(ps))
	for _, p := range ps {
		names = append(names, p.Name)
	}
	if len(names) == 0 {
		return Persona{}, fmt.Errorf("no persona named %q: none are defined (config \"personas\", .janetcode/personas/)", name)
	}
	return Persona{}, fmt.Errorf("no persona named %q (defined: %s)", name, strings.Join(names, ", "))
}

// Report is /personas: each persona, where it came from, and its contract.
func Report(ps []Persona, bad []string) string {
	if len(ps) == 0 && len(bad) == 0 {
		return "no personas defined. Add one under \"personas\" in the config file, or as .janetcode/personas/<name>.md or .json; the task tool takes persona: <name>"
	}
	var sb strings.Builder
	for _, p := range ps {
		fmt.Fprintf(&sb, "%s  (%s)  %s\n", p.Name, p.Source, p.Summary())
		if p.Model != "" || p.Effort != "" {
			fmt.Fprintf(&sb, "    model %s effort %s\n", orDash(p.Model), orDash(p.Effort))
		}
		for _, f := range p.Inputs {
			fmt.Fprintf(&sb, "    in:  %s - %s\n", f.Name, f.Description)
		}
		for _, f := range p.Outputs {
			fmt.Fprintf(&sb, "    out: %s - %s\n", f.Name, f.Description)
		}
	}
	for _, b := range bad {
		fmt.Fprintf(&sb, "skipped: %s\n", b)
	}
	return strings.TrimRight(sb.String(), "\n")
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
