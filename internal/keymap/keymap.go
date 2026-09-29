// Package keymap is the table of what the terminal interface's keys do
// at the prompt: each action has a name, a line of help and its default
// keys, and the config file's "keybindings" object can move any action to
// other keys. The defaults and the overrides go through the same
// resolution, so a key the user did not touch behaves exactly as it did
// before there was an override to make, and a conflict or an unknown
// action is refused when the config is loaded rather than discovered as a
// key that silently does nothing.
package keymap

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
)

// Action is the name of something a key can do at the prompt.
type Action string

// The actions a key can be bound to. The names are what the config file
// says, so they are part of the file format: rename one and old configs
// break.
const (
	Submit         Action = "submit"
	Newline        Action = "newline"
	Cancel         Action = "cancel"
	Interrupt      Action = "interrupt"
	Quit           Action = "quit"
	Expand         Action = "expand"
	Background     Action = "background"
	Steer          Action = "steer"
	Redraw         Action = "redraw"
	ModeCycle      Action = "mode_cycle"
	Agents         Action = "agents"
	Effort         Action = "effort"
	PasteImage     Action = "paste_image"
	Complete       Action = "complete"
	HistoryPrev    Action = "history_prev"
	HistoryNext    Action = "history_next"
	HistorySearch  Action = "history_search"
	ExternalEditor Action = "external_editor"
	PageUp         Action = "page_up"
	PageDown       Action = "page_down"
)

// Def is one action's entry in the table.
type Def struct {
	Action Action
	Keys   []string // the defaults
	Help   string
}

// Defs is every bindable action with its default keys, in the order the
// help sheet lists them.
var Defs = []Def{
	{Submit, []string{"enter"}, "send the prompt (or take the highlighted completion)"},
	{Newline, []string{"ctrl+j", "alt+enter"}, "insert a newline in the prompt"},
	{Cancel, []string{"esc"}, "cancel the turn, leave the history, or clear the input"},
	{Interrupt, []string{"ctrl+c"}, "cancel the turn or clear the input; twice when idle quits"},
	{Quit, []string{"ctrl+d"}, "quit, on an empty prompt"},
	{Expand, []string{"ctrl+o"}, "expand the last turn's folded output (again folds it)"},
	{Background, []string{"ctrl+g"}, "move the foreground command to the background as a job"},
	{Steer, []string{"ctrl+s"}, "hand what you typed to the running turn instead of queueing it"},
	{Redraw, []string{"ctrl+l"}, "redraw the screen"},
	{ModeCycle, []string{"shift+tab"}, "cycle ask → plan → auto → yolo"},
	{Agents, []string{"ctrl+t"}, "open the subagents overlay (/agents)"},
	{Effort, []string{"alt+e"}, "cycle how hard the model thinks (/effort)"},
	{PasteImage, []string{"ctrl+v"}, "paste a clipboard image into the prompt"},
	{Complete, []string{"tab"}, "complete a command, argument or @path; accept the suggested follow-up"},
	{HistoryPrev, []string{"up"}, "previous prompt (or move up in an open menu)"},
	{HistoryNext, []string{"down"}, "next prompt (or move down in an open menu)"},
	{HistorySearch, []string{"ctrl+r"}, "search the prompt history backwards as you type"},
	{ExternalEditor, []string{"ctrl+x ctrl+e"}, "write the prompt in $VISUAL or $EDITOR"},
	{PageUp, []string{"pgup", "ctrl+b", "ctrl+u"}, "scroll the transcript up (ctrl+b/u only on an empty prompt)"},
	{PageDown, []string{"pgdown", "ctrl+f"}, "scroll the transcript down (ctrl+f only on an empty prompt)"},
}

// Keys is one action's keys as the config file writes them: a string for
// one key, a list for several, "" or [] to unbind the action.
type Keys []string

// UnmarshalJSON takes either spelling.
func (k *Keys) UnmarshalJSON(b []byte) error {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		if one == "" {
			*k = Keys{}
		} else {
			*k = Keys{one}
		}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return fmt.Errorf("want a key or a list of keys, like \"ctrl+r\" or [\"ctrl+r\", \"alt+r\"]")
	}
	*k = Keys(many)
	return nil
}

// Overrides is the config file's "keybindings" object: action → keys.
type Overrides map[string]Keys

// Map is the resolved table: which action each key (or two-key chord,
// written "ctrl+x ctrl+e") runs, and which keys each action has.
type Map struct {
	byKey    map[string]Action
	byAction map[Action][]string
	prefixes map[string]bool // first keys of chords
}

// Default is the table with no overrides.
func Default() *Map {
	m, err := Resolve(nil)
	if err != nil {
		panic("keymap defaults do not resolve: " + err.Error())
	}
	return m
}

// Lookup is the action a key or chord runs.
func (m *Map) Lookup(key string) (Action, bool) {
	a, ok := m.byKey[key]
	return a, ok
}

// IsPrefix reports whether a key starts a chord, so the prompt waits for
// the second key rather than handing the first to the textarea.
func (m *Map) IsPrefix(key string) bool { return m.prefixes[key] }

// Keys is what is bound to an action, in the order given.
func (m *Map) Keys(a Action) []string { return m.byAction[a] }

// First is an action's first key, or "" when it is unbound: what a status
// line names when it tells you which key to press.
func (m *Map) First(a Action) string {
	if ks := m.byAction[a]; len(ks) > 0 {
		return ks[0]
	}
	return ""
}

// Label is an action's keys joined for the help sheet, "unbound" when
// there are none.
func (m *Map) Label(a Action) string {
	if ks := m.byAction[a]; len(ks) > 0 {
		return strings.Join(ks, " / ")
	}
	return "unbound"
}

// Changed lists the actions whose keys differ from the defaults, sorted,
// for the help sheet's "yours" line.
func (m *Map) Changed() []Action {
	var out []Action
	for _, d := range Defs {
		if strings.Join(d.Keys, "\x00") != strings.Join(m.byAction[d.Action], "\x00") {
			out = append(out, d.Action)
		}
	}
	return out
}

// Resolve applies overrides to the defaults. An overridden action loses
// all of its default keys and takes exactly the ones given; every other
// action keeps its defaults. It is an error to name an action that does
// not exist, to give a key bubbletea cannot produce, to bind a plain
// character (it could no longer be typed), or to leave one key on two
// actions — a key taken from another action's defaults has to be moved
// off that action too, in the same object, so a conflict is never
// settled by an order the user cannot see.
func Resolve(ov Overrides) (*Map, error) {
	known := map[Action]Def{}
	for _, d := range Defs {
		known[d.Action] = d
	}
	var bad []string
	for name := range ov {
		if _, ok := known[Action(name)]; !ok {
			bad = append(bad, name)
		}
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		return nil, fmt.Errorf("keybindings: unknown action %s (want one of: %s)", quoteList(bad), strings.Join(ActionNames(), ", "))
	}
	m := &Map{byKey: map[string]Action{}, byAction: map[Action][]string{}, prefixes: map[string]bool{}}
	for _, d := range Defs {
		keys := d.Keys
		if o, ok := ov[string(d.Action)]; ok {
			keys = nil
			for _, k := range o {
				norm, err := normalize(k)
				if err != nil {
					return nil, fmt.Errorf("keybindings: %s: %v", d.Action, err)
				}
				keys = append(keys, norm)
			}
		}
		for _, k := range keys {
			if prev, ok := m.byKey[k]; ok {
				if prev == d.Action {
					continue
				}
				return nil, fmt.Errorf("keybindings: %q is bound to both %s and %s; move one of them to another key (or unbind it with \"\")", k, prev, d.Action)
			}
			m.byKey[k] = d.Action
			m.byAction[d.Action] = append(m.byAction[d.Action], k)
		}
	}
	for k := range m.byKey {
		if first, _, chord := strings.Cut(k, " "); chord {
			m.prefixes[first] = true
		}
	}
	for p := range m.prefixes {
		if a, ok := m.byKey[p]; ok {
			return nil, fmt.Errorf("keybindings: %q is bound to %s and also starts a chord; a key cannot be both", p, a)
		}
	}
	return m, nil
}

// ActionNames is every action's name, in table order.
func ActionNames() []string {
	out := make([]string, len(Defs))
	for i, d := range Defs {
		out[i] = string(d.Action)
	}
	return out
}

// normalize checks one key or two-key chord and returns its canonical
// spelling (lower case, single spaces).
func normalize(spec string) (string, error) {
	parts := strings.Fields(strings.ToLower(spec))
	switch len(parts) {
	case 0:
		return "", fmt.Errorf("an empty key; use \"\" alone to unbind the action")
	case 1, 2:
	default:
		return "", fmt.Errorf("%q: a chord is at most two keys, like \"ctrl+x ctrl+e\"", spec)
	}
	for _, p := range parts {
		if err := validKey(p); err != nil {
			return "", fmt.Errorf("%q: %v", spec, err)
		}
	}
	return strings.Join(parts, " "), nil
}

// namedKeys are the key names bubbletea reports for keys that are not a
// printable character: "ctrl+r", "shift+tab", "pgup", "f5" and the rest.
var namedKeys = func() map[string]bool {
	out := map[string]bool{}
	for i := -200; i < 256; i++ {
		if s := tea.KeyType(i).String(); s != "" && s != "runes" && s != " " {
			out[s] = true
		}
	}
	return out
}()

func validKey(k string) error {
	base, alt := strings.CutPrefix(k, "alt+")
	if namedKeys[base] {
		return nil
	}
	if utf8.RuneCountInString(base) == 1 {
		if alt {
			return nil
		}
		return fmt.Errorf("a plain character could no longer be typed at the prompt; give it a modifier, like \"alt+%s\"", base)
	}
	return fmt.Errorf("not a key name (examples: ctrl+r, alt+e, shift+tab, f2, pgup, ctrl+x ctrl+e)")
}

func quoteList(names []string) string {
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = fmt.Sprintf("%q", n)
	}
	return strings.Join(q, ", ")
}
