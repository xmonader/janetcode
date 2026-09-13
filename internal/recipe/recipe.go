// Package recipe crystallises a turn that worked into a replayable
// program of real tool calls: the steps a session actually ran, kept as
// data so they can run again later with no model call and no tokens.
// Running one still goes through the same pipeline a model-driven call
// does — authorize, approvals, guards, checkpoints, the event log — which
// is why this package holds only the tool-call data and its
// transformations (extracting, parameterising, expanding, and the file
// format) and never runs anything itself: internal/agent's Agent.RunRecipe
// does that, through the same internals a turn uses.
//
// This package must not import internal/agent: the agent needs to accept
// a Recipe (to run it), so the dependency can only run one way.
package recipe

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/xmonader/janetcode/internal/fsutil"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/textfmt"
	"github.com/xmonader/janetcode/internal/tool"
)

// Param is a named substitution a saved recipe accepts. Default is the
// literal value it was cut from, used when Expand is not given one.
type Param struct {
	Name    string `json:"name"`
	Default string `json:"default"`
}

// Step is one real tool call, kept exactly as it would be made through
// Agent.runNested: the tool's registered name and its input, both taken
// from what the session already logged.
type Step struct {
	Tool   string          `json:"tool"`
	Input  json.RawMessage `json:"input"`
	Intent string          `json:"intent,omitempty"`
}

// Recipe is a named, saved program of steps, with the parameters it
// accepts and where it came from.
type Recipe struct {
	Name        string  `json:"name"`
	Description string  `json:"description,omitempty"`
	Params      []Param `json:"params,omitempty"`
	Steps       []Step  `json:"steps"`
	From        struct {
		Session string `json:"session,omitempty"`
		Turn    int    `json:"turn,omitempty"`
	} `json:"from,omitempty"`
}

// LatestToolTurn is the highest turn number that made any tool call, or 0
// when none did. FromEvents calls it for turn==0; save commands call it
// directly so they can use the same turn number for the recipe's From and
// its default description.
func LatestToolTurn(events []session.Event) int {
	max := 0
	for _, e := range events {
		if e.Type == session.TypeToolCall && e.Turn > max {
			max = e.Turn
		}
	}
	return max
}

// FromEvents extracts one turn's tool calls, in the order they ran,
// keeping only calls whose result was not an error and dropping tools
// that must not be replayed (see tool.NotReplayable). turn==0 means the
// latest turn that made any tool call.
func FromEvents(events []session.Event, turn int) ([]Step, error) {
	if turn == 0 {
		turn = LatestToolTurn(events)
	}
	if turn == 0 {
		return nil, errors.New("no tool calls found in this session")
	}
	var order []string
	calls := map[string]session.Event{}
	results := map[string]session.Event{}
	for _, e := range events {
		if e.Turn != turn {
			continue
		}
		switch e.Type {
		case session.TypeToolCall:
			if _, seen := calls[e.CallID]; !seen {
				order = append(order, e.CallID)
			}
			calls[e.CallID] = e
		case session.TypeToolResult:
			results[e.CallID] = e
		}
	}
	if len(order) == 0 {
		return nil, fmt.Errorf("turn %d made no tool calls", turn)
	}
	var steps []Step
	for _, id := range order {
		call := calls[id]
		if tool.NotReplayable[call.Name] {
			continue
		}
		res, ok := results[id]
		if !ok || res.IsError {
			continue
		}
		steps = append(steps, Step{Tool: call.Name, Input: json.RawMessage(call.Arguments), Intent: call.Intent})
	}
	if len(steps) == 0 {
		return nil, fmt.Errorf("turn %d has no replayable tool calls (steps that failed, or that only affect the conversation, do not count)", turn)
	}
	return steps, nil
}

// TurnSummary is a turn's own description: the first line of its first
// real user message, clipped. It is /recipe save's default description
// when the user does not give one with --desc.
func TurnSummary(events []session.Event, turn int) string {
	for _, e := range events {
		if e.Turn != turn || e.Type != session.TypeUserMessage || e.Message == nil {
			continue
		}
		if t := strings.TrimSpace(userText(e.Message)); t != "" && !session.Synthetic(t) {
			return textfmt.FirstLine(t, 80)
		}
	}
	return ""
}

// userText is a message's text, excluding blocks the harness itself wrote
// in (marked Synthetic), the same exclusion session.Synthetic gives a
// whole message.
func userText(m *llm.Message) string {
	var b strings.Builder
	for _, blk := range m.Blocks {
		if blk.Type == llm.BlockText && !blk.Synthetic {
			b.WriteString(blk.Text)
		}
	}
	return b.String()
}

// Parameterise replaces, in each step's input, every exact occurrence of a
// given literal value with {{name}} — inside JSON string values only,
// never inside a key — and returns the rewritten steps together with the
// Param list Save should record (Default holding the literal it was cut
// from). A value not found in any step is left out of the returned list:
// a recipe should not advertise a parameter that does nothing.
func Parameterise(steps []Step, params map[string]string) ([]Step, []Param) {
	if len(params) == 0 {
		return steps, nil
	}
	// Longest value first: substituting a short value first can eat part
	// of a longer one that was also asked for ("foo" inside "/tmp/foo").
	names := make([]string, 0, len(params))
	for n, v := range params {
		if strings.TrimSpace(v) != "" {
			names = append(names, n)
		}
	}
	sort.Slice(names, func(i, j int) bool { return len(params[names[i]]) > len(params[names[j]]) })

	used := map[string]bool{}
	out := make([]Step, len(steps))
	for i, st := range steps {
		var v any
		if json.Unmarshal(st.Input, &v) != nil {
			out[i] = st
			continue
		}
		v = substitute(v, names, params, used)
		data, err := json.Marshal(v)
		if err != nil {
			out[i] = st
			continue
		}
		out[i] = Step{Tool: st.Tool, Input: data, Intent: st.Intent}
	}
	var ps []Param
	for _, n := range names {
		if used[n] {
			ps = append(ps, Param{Name: n, Default: params[n]})
		}
	}
	return out, ps
}

func substitute(v any, names []string, params map[string]string, used map[string]bool) any {
	switch t := v.(type) {
	case string:
		for _, n := range names {
			val := params[n]
			if !strings.Contains(t, val) {
				continue
			}
			t = strings.ReplaceAll(t, val, "{{"+n+"}}")
			used[n] = true
		}
		return t
	case map[string]any:
		for k, vv := range t {
			t[k] = substitute(vv, names, params, used)
		}
		return t
	case []any:
		for i, vv := range t {
			t[i] = substitute(vv, names, params, used)
		}
		return t
	default:
		return v
	}
}

// placeholderRe matches {{name}} inside a step's JSON string values.
var placeholderRe = regexp.MustCompile(`\{\{\s*([A-Za-z0-9_-]+)\s*\}\}`)

// Expand substitutes every {{name}} in the recipe's steps with a value:
// args[name] when given, else the parameter's own Default. It errors on
// an arg naming a parameter the recipe does not have, on a parameter with
// neither an arg nor a non-empty default, and on a {{name}} in a step
// that names no declared parameter at all. Substitution happens on the
// decoded string value and the step is re-marshalled, so a value
// containing quotes or newlines is JSON-escaped correctly.
func Expand(r Recipe, args map[string]string) ([]Step, error) {
	declared := make(map[string]string, len(r.Params))
	for _, p := range r.Params {
		declared[p.Name] = p.Default
	}
	for k := range args {
		if _, ok := declared[k]; !ok {
			return nil, fmt.Errorf("recipe %s has no parameter %q", r.Name, k)
		}
	}
	values := make(map[string]string, len(declared))
	var missing []string
	for name, def := range declared {
		if v, ok := args[name]; ok {
			values[name] = v
		} else if def != "" {
			values[name] = def
		} else {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("recipe %s: missing value for %s (pass name=value)", r.Name, strings.Join(missing, ", "))
	}
	out := make([]Step, len(r.Steps))
	for i, st := range r.Steps {
		data, err := expandTemplate(st.Input, values)
		if err != nil {
			return nil, fmt.Errorf("step %d (%s): %w", i+1, st.Tool, err)
		}
		out[i] = Step{Tool: st.Tool, Input: data, Intent: st.Intent}
	}
	return out, nil
}

func expandTemplate(raw json.RawMessage, values map[string]string) (json.RawMessage, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	v, err := expandValue(v, values)
	if err != nil {
		return nil, err
	}
	return json.Marshal(v)
}

func expandValue(v any, values map[string]string) (any, error) {
	switch t := v.(type) {
	case string:
		var err error
		out := placeholderRe.ReplaceAllStringFunc(t, func(m string) string {
			if err != nil {
				return m
			}
			name := placeholderRe.FindStringSubmatch(m)[1]
			val, ok := values[name]
			if !ok {
				err = fmt.Errorf("unknown parameter %q", name)
				return m
			}
			return val
		})
		if err != nil {
			return nil, err
		}
		return out, nil
	case map[string]any:
		for k, vv := range t {
			nv, err := expandValue(vv, values)
			if err != nil {
				return nil, err
			}
			t[k] = nv
		}
		return t, nil
	case []any:
		for i, vv := range t {
			nv, err := expandValue(vv, values)
			if err != nil {
				return nil, err
			}
			t[i] = nv
		}
		return t, nil
	default:
		return v, nil
	}
}

// recipesDir is where a project's recipes live: meant to be committed,
// the same way .janetcode/commands and .janetcode/evals are.
func recipesDir(root string) string { return filepath.Join(root, ".janetcode", "recipes") }

// Save writes a recipe to <root>/.janetcode/recipes/<name>.json. It
// refuses to overwrite an existing file: a recipe is meant to be reviewed
// and committed, and silently replacing one destroys that review.
func Save(root string, r Recipe) (path string, err error) {
	name := strings.TrimSpace(r.Name)
	if name == "" {
		return "", errors.New("recipe needs a name")
	}
	if len(r.Steps) == 0 {
		return "", errors.New("recipe has no steps")
	}
	dir := recipesDir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path = filepath.Join(dir, name+".json")
	if _, statErr := os.Stat(path); statErr == nil {
		return "", fmt.Errorf("%s already exists; pass a different name", path)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return "", statErr
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", err
	}
	if err := fsutil.WriteFileAtomic(path, append(data, '\n'), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// Load reads a saved recipe by name.
func Load(root, name string) (Recipe, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Recipe{}, errors.New("recipe name is empty")
	}
	data, err := os.ReadFile(filepath.Join(recipesDir(root), name+".json"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Recipe{}, fmt.Errorf("no recipe named %q (/recipe list shows what is saved)", name)
		}
		return Recipe{}, err
	}
	var r Recipe
	if err := json.Unmarshal(data, &r); err != nil {
		return Recipe{}, fmt.Errorf("%s: %w", name, err)
	}
	if r.Name == "" {
		r.Name = name
	}
	return r, nil
}

// List returns every saved recipe, by name. A file that fails to parse is
// skipped rather than failing the whole listing.
func List(root string) ([]Recipe, error) {
	entries, err := os.ReadDir(recipesDir(root))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Recipe
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		r, err := Load(root, strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// CutDesc pulls "--desc <text>" out of /recipe save's argument text: like
// /eval keep's --check, it takes the rest of the line, so both front-ends
// call this before the simpler k=v tokenising ParseSaveArgs does, rather
// than each cutting it out its own way.
func CutDesc(arg string) (rest, desc string) {
	i := strings.Index(arg, "--desc")
	if i < 0 {
		return arg, ""
	}
	return strings.TrimSpace(arg[:i]), strings.TrimSpace(arg[i+len("--desc"):])
}

// ParseSaveArgs reads the argument to /recipe save: `<name> [--turn n]
// [k=v ...]`. Params come back as a map ready for Parameterise.
func ParseSaveArgs(arg string) (name string, turn int, params map[string]string, err error) {
	const usage = "usage: /recipe save <name> [--turn n] [k=v ...]"
	fields := strings.Fields(arg)
	if len(fields) == 0 {
		return "", 0, nil, errors.New(usage)
	}
	name = fields[0]
	params = map[string]string{}
	for i := 1; i < len(fields); i++ {
		f := fields[i]
		switch {
		case f == "--turn":
			i++
			if i >= len(fields) {
				return "", 0, nil, fmt.Errorf("--turn needs a number; %s", usage)
			}
			n, convErr := parsePositiveInt(fields[i])
			if convErr != nil {
				return "", 0, nil, fmt.Errorf("--turn: %q is not a turn number", fields[i])
			}
			turn = n
		case strings.Contains(f, "="):
			k, v, _ := strings.Cut(f, "=")
			if k == "" {
				return "", 0, nil, fmt.Errorf("bad parameter %q", f)
			}
			params[k] = v
		default:
			return "", 0, nil, fmt.Errorf("unexpected argument %q; %s", f, usage)
		}
	}
	return name, turn, params, nil
}

// ParseRunArgs reads the argument to /recipe run: `<name> [--dry-run]
// [k=v ...]`.
func ParseRunArgs(arg string) (name string, args map[string]string, dryRun bool, err error) {
	const usage = "usage: /recipe run <name> [--dry-run] [k=v ...]"
	fields := strings.Fields(arg)
	if len(fields) == 0 {
		return "", nil, false, errors.New(usage)
	}
	name = fields[0]
	args = map[string]string{}
	for _, f := range fields[1:] {
		switch {
		case f == "--dry-run":
			dryRun = true
		case strings.Contains(f, "="):
			k, v, _ := strings.Cut(f, "=")
			if k == "" {
				return "", nil, false, fmt.Errorf("bad argument %q", f)
			}
			args[k] = v
		default:
			return "", nil, false, fmt.Errorf("unexpected argument %q; %s", f, usage)
		}
	}
	return name, args, dryRun, nil
}

func parsePositiveInt(s string) (int, error) {
	n := 0
	if s == "" {
		return 0, errors.New("empty")
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, errors.New("not a number")
		}
		n = n*10 + int(r-'0')
	}
	return n, nil
}
