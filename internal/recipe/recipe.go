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
	"strconv"
	"strings"

	"github.com/xmonader/janetcode/internal/fsutil"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/slash/args"
	"github.com/xmonader/janetcode/internal/textfmt"
	"github.com/xmonader/janetcode/internal/trust"
)

// Param is a named substitution a saved recipe accepts. Default is the
// literal value it was cut from, used when Expand is not given one.
type Param struct {
	Name    string `json:"name"`
	Default string `json:"default"`
}

// Expect is a precondition RunRecipe checks before a step is allowed to
// run, so a replay never mutates something it has not verified matches
// what the recording expected.
type Expect struct {
	// Absent means the step's target path must either not exist yet, or
	// already hold exactly the content this step is about to write (a
	// harmless no-op replay) — anything else fails the step rather than
	// overwriting it. Set on a write_file step that created a new file
	// when it was recorded: there is no "before" content to anchor a
	// safer form on, so the file's absence (or byte-for-byte agreement
	// with what would be written anyway) is all that can be verified.
	Absent bool `json:"absent,omitempty"`
}

// Step is one real tool call, kept exactly as it would be made through
// Agent.runNested: the tool's registered name and its input, both taken
// from what the session already logged.
type Step struct {
	Tool   string          `json:"tool"`
	Input  json.RawMessage `json:"input"`
	Intent string          `json:"intent,omitempty"`
	// Expect, when set, is checked before the step runs; see Expect.
	Expect *Expect `json:"expect,omitempty"`
	// CallID is the tool-call id this step was extracted from, kept only
	// long enough for the agent's own save-time conversion (see
	// internal/agent's Agent.RecipeSteps) to match a step against the
	// session's change provenance for the same call. It never reaches a
	// saved recipe file.
	CallID string `json:"-"`
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
// that must not be replayed (see NotReplayable). turn==0 means the
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
		if NotReplayable[call.Name] {
			continue
		}
		res, ok := results[id]
		if !ok || res.IsError {
			continue
		}
		steps = append(steps, Step{Tool: call.Name, Input: json.RawMessage(call.Arguments), Intent: call.Intent, CallID: id})
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
//
// Braces the recorded calls themselves contain ("{{ message }}" in a Vue
// or Jinja template the turn wrote) are escaped as they are saved, so
// Expand gives them back as text rather than reading them as parameters:
// a recipe that wrote a template failed every replay on an "unknown
// parameter", and one whose template variable shared a parameter's name
// had the template rewritten.
func Parameterise(steps []Step, params map[string]string) ([]Step, []Param) {
	// Longest value first: substituting a short value first can eat part
	// of a longer one that was also asked for ("foo" inside "/tmp/foo").
	// Ties break on the name, and the sort is stable, so two parameters
	// with equally long values always substitute in the same order — the
	// slice comes out of a map, so without this a saved recipe's steps
	// and its params list could differ between two identical /recipe save
	// invocations.
	names := make([]string, 0, len(params))
	for n, v := range params {
		if strings.TrimSpace(v) != "" {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	sort.SliceStable(names, func(i, j int) bool { return len(params[names[i]]) > len(params[names[j]]) })

	used := map[string]bool{}
	out := make([]Step, len(steps))
	for i, st := range steps {
		var v any
		if json.Unmarshal(st.Input, &v) != nil {
			out[i] = st
			continue
		}
		// A step with nothing to rewrite keeps its bytes: re-marshalling
		// reorders keys, escapes <, > and & and rounds large numbers.
		changed := false
		v = substitute(v, names, params, used, &changed)
		if !changed {
			out[i] = Step{Tool: st.Tool, Input: st.Input, Intent: st.Intent, Expect: st.Expect}
			continue
		}
		data, err := json.Marshal(v)
		if err != nil {
			out[i] = st
			continue
		}
		out[i] = Step{Tool: st.Tool, Input: data, Intent: st.Intent, Expect: st.Expect}
	}
	var ps []Param
	for _, n := range names {
		if used[n] {
			ps = append(ps, Param{Name: n, Default: params[n]})
		}
	}
	return out, ps
}

// escapedBraces is how a literal "{{" in a recorded call is saved: Expand
// turns it back into "{{" and never reads it as a placeholder.
const escapedBraces = `{{"{{"}}`

func substitute(v any, names []string, params map[string]string, used map[string]bool, changed *bool) any {
	switch t := v.(type) {
	case string:
		// One pass over the recorded text: at each position the longest
		// parameter value wins, then a literal "{{" is escaped. Replacing
		// value by value on the text already rewritten could match a
		// shorter value inside a placeholder an earlier one had put there.
		var b strings.Builder
	scan:
		for i := 0; i < len(t); {
			for _, n := range names {
				if val := params[n]; strings.HasPrefix(t[i:], val) {
					b.WriteString("{{" + n + "}}")
					used[n] = true
					i += len(val)
					continue scan
				}
			}
			if strings.HasPrefix(t[i:], "{{") {
				b.WriteString(escapedBraces)
				i += 2
				continue
			}
			b.WriteByte(t[i])
			i++
		}
		if b.Len() != len(t) || b.String() != t {
			*changed = true
		}
		return b.String()
	case map[string]any:
		for k, vv := range t {
			t[k] = substitute(vv, names, params, used, changed)
		}
		return t
	case []any:
		for i, vv := range t {
			t[i] = substitute(vv, names, params, used, changed)
		}
		return t
	default:
		return v
	}
}

// placeholderRe matches {{name}} inside a step's JSON string values, or
// an escaped literal "{{" (see escapedBraces), in one pass so that
// neither a substituted value nor an unescaped brace is read again.
var placeholderRe = regexp.MustCompile(regexp.QuoteMeta(escapedBraces) + `|\{\{\s*([A-Za-z0-9_-]+)\s*\}\}`)

// paramNameRe is the name placeholderRe can read back.
var paramNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

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
		out[i] = Step{Tool: st.Tool, Input: data, Intent: st.Intent, Expect: st.Expect}
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
			if m == escapedBraces {
				return "{{"
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

// checkName refuses a recipe name that is not a plain file name. The name
// becomes <name>.json under the recipes directory, so "../../x" (typed at
// /recipe save or passed to --recipe) wrote or read outside it.
func checkName(name string) error {
	if strings.ContainsAny(name, `/\`) || name == "." || name == ".." || !filepath.IsLocal(name+".json") {
		return fmt.Errorf("recipe name %q must be a plain name, without path separators", name)
	}
	return nil
}

// Save writes a recipe to <root>/.janetcode/recipes/<name>.json. It
// refuses to overwrite an existing file: a recipe is meant to be reviewed
// and committed, and silently replacing one destroys that review.
func Save(root string, r Recipe) (path string, err error) {
	name := strings.TrimSpace(r.Name)
	if name == "" {
		return "", errors.New("recipe needs a name")
	}
	if err := checkName(name); err != nil {
		return "", err
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
	trust.SelfWrote(root)
	return path, nil
}

// Load reads a saved recipe by name.
func Load(root, name string) (Recipe, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Recipe{}, errors.New("recipe name is empty")
	}
	if err := checkName(name); err != nil {
		return Recipe{}, err
	}
	data, err := os.ReadFile(filepath.Join(recipesDir(root), name+".json"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Recipe{}, notFound(root, name)
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

// ErrNoRecipe is Load's error for a name nothing is saved under.
var ErrNoRecipe = errors.New("no such recipe")

// notFound names what is saved, so the answer works from the CLI as well
// as from a session: it used to point at /recipe list, which a shell
// running --recipe does not have.
func notFound(root, name string) error {
	have, _ := List(root)
	near := make([]string, 0, len(have))
	for _, r := range have {
		near = append(near, r.Name)
	}
	msg := fmt.Sprintf("no recipe named %q", name)
	switch {
	case len(near) == 0:
		msg += "; none are saved in this project (a session saves one with /recipe save)"
	case len(near) <= 8:
		msg += "; saved: " + strings.Join(near, ", ")
	default:
		msg += fmt.Sprintf("; %d are saved, e.g. %s", len(near), strings.Join(near[:8], ", "))
	}
	return noRecipe(msg)
}

// noRecipe is ErrNoRecipe with the message that says what is saved.
type noRecipe string

func (e noRecipe) Error() string        { return string(e) }
func (e noRecipe) Is(target error) bool { return target == ErrNoRecipe }

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
// /eval keep's --check, it takes the rest of the line, so it is cut out
// before the k=v tokenising ParseSaveArgs does.
func CutDesc(arg string) (rest, desc string) {
	t, _ := args.Tokenize(arg, args.Spec{Rest: []string{"--desc"}})
	return strings.Join(t.Positional, " "), t.Flags["--desc"]
}

// ParseSaveArgs reads the argument to /recipe save: `<name> [--turn n]
// [k=v ...]`. Params come back as a map ready for Parameterise.
func ParseSaveArgs(arg string) (name string, turn int, params map[string]string, err error) {
	const usage = "usage: /recipe save <name> [--turn n] [k=v ...]"
	name, rest := args.CutWord(arg)
	if name == "" {
		return "", 0, nil, errors.New(usage)
	}
	t, err := args.Tokenize(rest, args.Spec{Value: []string{"--turn"}, KV: true})
	if err != nil {
		return "", 0, nil, argError(err, "parameter", usage)
	}
	if len(t.Positional) > 0 {
		return "", 0, nil, fmt.Errorf("unexpected argument %q; %s", t.Positional[0], usage)
	}
	if v, ok := t.Flags["--turn"]; ok {
		n, convErr := strconv.ParseUint(v, 10, 0)
		if convErr != nil {
			return "", 0, nil, fmt.Errorf("--turn: %q is not a turn number", v)
		}
		turn = int(n)
	}
	// A parameter is saved as {{name}}, and Expand reads back only the
	// names placeholderRe takes: any other would be saved, never
	// substituted, and replayed as the literal "{{file.path}}".
	for k := range t.KV {
		if !paramNameRe.MatchString(k) {
			return "", 0, nil, fmt.Errorf("parameter name %q: use letters, digits, '_' and '-'", k)
		}
	}
	return name, turn, t.KV, nil
}

// ParseRunArgs reads the argument to /recipe run: `<name> [--dry-run]
// [k=v ...]`.
func ParseRunArgs(arg string) (name string, params map[string]string, dryRun bool, err error) {
	const usage = "usage: /recipe run <name> [--dry-run] [k=v ...]"
	name, rest := args.CutWord(arg)
	if name == "" {
		return "", nil, false, errors.New(usage)
	}
	t, err := args.Tokenize(rest, args.Spec{Bool: []string{"--dry-run"}, KV: true})
	if err != nil {
		return "", nil, false, argError(err, "argument", usage)
	}
	if len(t.Positional) > 0 {
		return "", nil, false, fmt.Errorf("unexpected argument %q; %s", t.Positional[0], usage)
	}
	return name, t.KV, t.Has("--dry-run"), nil
}

// argError words an args.Tokenize error the way /recipe always has:
// noun names what a k=v token is ("parameter" for save, "argument" for
// run).
func argError(err error, noun, usage string) error {
	var mv *args.MissingValueError
	if errors.As(err, &mv) {
		return fmt.Errorf("%s needs a number; %s", mv.Flag, usage)
	}
	var ek *args.EmptyKeyError
	if errors.As(err, &ek) {
		return fmt.Errorf("bad %s %q", noun, ek.Token)
	}
	return err
}
