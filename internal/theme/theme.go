// Package theme names the full palette the terminal UI draws with. Before
// this package "theme" meant only whether the background was light or dark
// and which side of an adaptive colour to use; a Theme now names every one
// of the colour roles styles.go derives its lipgloss styles from, the code
// styling knob mdstyle.go needs, and the transcript's left gutter width.
//
// The package is deliberately free of lipgloss and of the tui package: it
// is pure data plus validation, so config.Load can validate a user's theme
// (unknown role, bad colour, missing role) and turn a bad one into
// config.ErrBadConfig at load, while the tui package converts a resolved
// Theme into concrete styles. Both sides agree on one list of roles.
package theme

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Roles are the colour slots every theme fills. The names match the
// json keys a user writes and the colXxx variables styles.go keeps, so a
// theme file reads the way the palette comment in styles.go describes it.
//
// A change here is a change to the palette the whole interface draws, so
// it is the single list both the builtins and the loader are checked
// against: an unknown key in a user theme is an error, and a builtin that
// forgets a role fails TestBuiltinsAreComplete.
const (
	RoleAccent       = "accent"        // headings, active border, links, prompt caret
	RoleDim          = "dim"           // secondary body text, tool detail
	RoleFaint        = "faint"         // the quietest text: hints, section labels
	RoleWarn         = "warn"          // warnings, the permission box
	RoleErr          = "err"           // errors and failed turns
	RoleOK           = "ok"            // success, ticks, added-file tags
	RoleUser         = "user"          // the line the user typed
	RoleTool         = "tool"          // tool names, inline code
	RoleHeadBg       = "head_bg"       // header/status bar background
	RoleHeadFg       = "head_fg"       // header/status bar text
	RoleBorder       = "border"        // an input box the model owns
	RoleBorderActive = "border_active" // an input box waiting for you
	RoleDiffAdd      = "diff_add"      // added lines in a diff
	RoleDiffDel      = "diff_del"      // removed lines in a diff
)

// Roles is the ordered list of every colour slot a complete theme fills.
var Roles = []string{
	RoleAccent, RoleDim, RoleFaint, RoleWarn, RoleErr, RoleOK, RoleUser,
	RoleTool, RoleHeadBg, RoleHeadFg, RoleBorder, RoleBorderActive,
	RoleDiffAdd, RoleDiffDel,
}

// roleSet is Roles as a set, for the unknown-key check.
var roleSet = func() map[string]bool {
	m := make(map[string]bool, len(Roles))
	for _, r := range Roles {
		m[r] = true
	}
	return m
}()

// DefaultGutter is the transcript's left gutter when a theme does not set
// one: two columns, so the top-level lines (the ✓ tool, · thought, ❯
// prompt) step in from the edge and the body indent steps in from them.
const DefaultGutter = 2

// Color is one role's colour. It is either a single value used in both
// terminal backgrounds, or a light/dark pair when the theme wants to
// adapt. A value is a "#rgb"/"#rrggbb" hex string or a 0–255 ANSI palette
// index (a string like "12"); an ANSI index defers to the terminal's own
// sixteen colours, exactly as the ANSI-16 fallback in styles.go does.
type Color struct {
	Light string
	Dark  string
}

// solid is a Color that is the same in both modes.
func solid(s string) Color { return Color{Light: s, Dark: s} }

// adapt is a Color that differs by mode.
func adapt(light, dark string) Color { return Color{Light: light, Dark: dark} }

// For returns the value for the given mode.
func (c Color) For(dark bool) string {
	if dark {
		return c.Dark
	}
	return c.Light
}

// Theme is a full named palette.
type Theme struct {
	// Name is the key it is selected by; it is set from the map key or the
	// file name, never from the file's own body.
	Name string
	// Desc is the one line shown beside the name in the /theme picker.
	Desc string
	// Dark says whether this is a dark theme. It decides two things: which
	// default a background probe picks (a dark terminal gets the dark
	// builtin), and — when the theme is selected by name — which mode the
	// background is pinned to, because a single-mode theme has only that
	// side of every Color filled. Selecting a dark-only theme therefore
	// pins the terminal to the dark assumption.
	Dark bool
	// Base, when set, names another theme this one inherits every unset
	// role from, so a user theme can override two colours and keep the
	// rest. Resolved (flattened) by Build.
	Base string
	// Gutter is the transcript left gutter in columns; nil means
	// DefaultGutter. A dense theme may set it to 0.
	Gutter *int
	// Roles maps each role name to its colour. After Build every required
	// role is present.
	Roles map[string]Color
	// Builtin marks a shipped theme, so the loader can report which name a
	// user theme is overriding and the picker can group them.
	Builtin bool
}

// GutterWidth returns the theme's gutter, or the default.
func (t Theme) GutterWidth() int {
	if t.Gutter == nil {
		return DefaultGutter
	}
	return *t.Gutter
}

// Get returns the colour for a role (zero Color if absent, which a
// validated theme never is).
func (t Theme) Get(role string) Color { return t.Roles[role] }

// Adaptive reports whether any role differs between light and dark, i.e.
// the theme serves both terminal backgrounds. A single-mode theme (every
// role the same in both) pins the background when selected; an adaptive
// one follows the terminal.
func (t Theme) Adaptive() bool {
	for _, c := range t.Roles {
		if c.Light != c.Dark {
			return true
		}
	}
	return false
}

// Set is a resolved, validated collection of themes selectable by name.
type Set struct {
	byName map[string]Theme
	names  []string // sorted; builtins first is not assumed, callers sort as they like
}

// Names lists every theme name, builtins and user themes, sorted.
func (s *Set) Names() []string {
	if s == nil {
		return nil
	}
	return append([]string(nil), s.names...)
}

// All returns every theme, sorted by name.
func (s *Set) All() []Theme {
	if s == nil {
		return nil
	}
	out := make([]Theme, 0, len(s.names))
	for _, n := range s.names {
		out = append(out, s.byName[n])
	}
	return out
}

// Get returns the theme by name.
func (s *Set) Get(name string) (Theme, bool) {
	if s == nil {
		return Theme{}, false
	}
	t, ok := s.byName[name]
	return t, ok
}

// Has reports whether the set knows a theme by that name.
func (s *Set) Has(name string) bool {
	if s == nil {
		return false
	}
	_, ok := s.byName[name]
	return ok
}

// Default returns the theme used when the config names auto/dark/light or
// nothing: the "default" builtin, whose colours are the ones janetcode has
// always drawn so an upgrade changes nothing on screen.
func (s *Set) Default() Theme {
	if s != nil {
		if t, ok := s.byName[DefaultName]; ok {
			return t
		}
	}
	return builtin(DefaultName)
}

// DefaultName is the builtin whose palette is the historical one.
const DefaultName = "default"

// Build merges the builtins with the user themes (user themes override a
// builtin of the same name), resolves every base inheritance and validates
// the result. A user theme with an unknown role key, a bad colour, an
// unknown base, or (after inheritance) a missing role is an error naming
// the theme and the field — the caller wraps it in ErrBadConfig. userThemes
// may be nil, in which case Build returns just the builtins.
func Build(userThemes map[string]Theme) (*Set, error) {
	// raw holds every theme before base flattening: builtins first, then
	// user themes on top (a user theme replaces the builtin it names).
	raw := map[string]Theme{}
	for _, t := range Builtins() {
		raw[t.Name] = t
	}
	for name, t := range userThemes {
		t.Name = name
		t.Builtin = false
		// A user theme may declare unknown roles; catch it before merging.
		for role := range t.Roles {
			if !roleSet[role] {
				return nil, fmt.Errorf("theme %q: unknown role %q (roles: %s)", name, role, strings.Join(Roles, ", "))
			}
		}
		for role, c := range t.Roles {
			if err := validColor(c.Light); err != nil {
				return nil, fmt.Errorf("theme %q: role %q: %v", name, role, err)
			}
			if err := validColor(c.Dark); err != nil {
				return nil, fmt.Errorf("theme %q: role %q: %v", name, role, err)
			}
		}
		raw[name] = t
	}

	set := &Set{byName: map[string]Theme{}}
	for name := range raw {
		resolved, err := flatten(name, raw, nil)
		if err != nil {
			return nil, err
		}
		if err := requireComplete(resolved); err != nil {
			return nil, err
		}
		set.byName[name] = resolved
	}
	for n := range set.byName {
		set.names = append(set.names, n)
	}
	sort.Strings(set.names)
	return set, nil
}

// flatten resolves a theme's base chain into a single theme with every
// role its bases and it define. seen guards against a base cycle.
func flatten(name string, raw map[string]Theme, seen []string) (Theme, error) {
	t, ok := raw[name]
	if !ok {
		return Theme{}, fmt.Errorf("theme %q: base %q is not a known theme", strings.Join(seen, "→"), name)
	}
	for _, s := range seen {
		if s == name {
			return Theme{}, fmt.Errorf("theme %q: base chain is a cycle (%s→%s)", seen[0], strings.Join(seen, "→"), name)
		}
	}
	if t.Base == "" {
		// Copy the roles map so callers never share the builtin's.
		out := t
		out.Roles = cloneRoles(t.Roles)
		return out, nil
	}
	base, err := flatten(t.Base, raw, append(seen, name))
	if err != nil {
		return Theme{}, err
	}
	// The child wins for every role it sets; unset roles and unset meta
	// come from the base.
	merged := base
	merged.Name = t.Name
	merged.Builtin = t.Builtin
	merged.Desc = t.Desc
	merged.Base = t.Base
	merged.Dark = t.Dark
	if t.Gutter != nil {
		merged.Gutter = t.Gutter
	}
	merged.Roles = cloneRoles(base.Roles)
	for role, c := range t.Roles {
		merged.Roles[role] = c
	}
	return merged, nil
}

func cloneRoles(in map[string]Color) map[string]Color {
	out := make(map[string]Color, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// requireComplete checks a flattened theme fills every role with a value
// for both modes.
func requireComplete(t Theme) error {
	for _, role := range Roles {
		c, ok := t.Roles[role]
		if !ok {
			return fmt.Errorf("theme %q: missing role %q", t.Name, role)
		}
		if c.Light == "" || c.Dark == "" {
			return fmt.Errorf("theme %q: role %q has no colour", t.Name, role)
		}
	}
	return nil
}

// validColor accepts a hex colour (#rgb or #rrggbb) or an ANSI palette
// index (0–255 as a decimal string). Anything else is rejected at load so
// a typo in a theme file is caught before it reaches lipgloss, which would
// silently render the mistake as no colour at all.
func validColor(s string) error {
	if s == "" {
		return fmt.Errorf("empty colour")
	}
	if strings.HasPrefix(s, "#") {
		h := s[1:]
		if len(h) != 3 && len(h) != 6 {
			return fmt.Errorf("colour %q: hex must be #rgb or #rrggbb", s)
		}
		for _, r := range h {
			if !isHex(r) {
				return fmt.Errorf("colour %q: %q is not a hex digit", s, r)
			}
		}
		return nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 || n > 255 {
		return fmt.Errorf("colour %q: want a #rrggbb hex or a 0–255 ANSI index", s)
	}
	return nil
}

func isHex(r rune) bool {
	return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
}
