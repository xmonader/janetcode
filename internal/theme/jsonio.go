package theme

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// A theme in a config file or a theme file is a small object: a
// description, whether it is dark, an optional base to inherit from, an
// optional gutter, and the role→colour map. A role's colour is written
// either as one string (used in both terminal backgrounds) or as an object
// {"light": …, "dark": …} when it should adapt.
//
//	{
//	  "description": "my warm dark theme",
//	  "dark": true,
//	  "base": "gruvbox",
//	  "gutter": 2,
//	  "roles": {
//	    "accent": "#fe8019",
//	    "dim":    {"light": "#3c3836", "dark": "#d5c4a1"}
//	  }
//	}
type themeJSON struct {
	Description string           `json:"description"`
	Dark        *bool            `json:"dark"`
	Base        string           `json:"base,omitempty"`
	Gutter      *int             `json:"gutter,omitempty"`
	Roles       map[string]Color `json:"roles"`
}

// UnmarshalJSON accepts a role colour as either a plain string (both
// modes) or a {"light","dark"} object. A partial object (only "dark", say)
// is allowed: the missing side stays empty, so a theme with a base can set
// just the mode it cares about and inherit the other.
func (c *Color) UnmarshalJSON(b []byte) error {
	b = []byte(strings.TrimSpace(string(b)))
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*c = solid(s)
		return nil
	}
	var pair struct {
		Light string `json:"light"`
		Dark  string `json:"dark"`
	}
	if err := json.Unmarshal(b, &pair); err != nil {
		return fmt.Errorf("colour must be a string or {\"light\",\"dark\"}: %v", err)
	}
	if pair.Light == "" && pair.Dark == "" {
		return fmt.Errorf("colour object needs \"light\", \"dark\" or both")
	}
	// The missing side is left empty here and filled when the theme is
	// built (see flatten): from the base's colour for the role when there
	// is a base, from the other side when there is not. Filling it here
	// from the other side left the base nothing to fill, so {"dark": …}
	// on a base replaced the base's light colour with a dark-mode one.
	c.Light, c.Dark = pair.Light, pair.Dark
	return nil
}

// MarshalJSON writes a Color as a bare string when both modes match, or a
// {"light","dark"} object when they differ, so a round-trip is readable.
func (c Color) MarshalJSON() ([]byte, error) {
	if c.Light == c.Dark {
		return json.Marshal(c.Light)
	}
	return json.Marshal(struct {
		Light string `json:"light"`
		Dark  string `json:"dark"`
	}{c.Light, c.Dark})
}

// ParseTheme reads one theme object (from a config map value or a theme
// file) under the given name. It does not validate colours or
// completeness — Build does, so every source is held to one standard.
func ParseTheme(name string, data []byte) (Theme, error) {
	var tj themeJSON
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&tj); err != nil {
		return Theme{}, fmt.Errorf("theme %q: %v", name, err)
	}
	return Theme{
		Name:      name,
		Desc:      tj.Description,
		Dark:      tj.Dark != nil && *tj.Dark,
		darkUnset: tj.Dark == nil,
		Base:      tj.Base,
		Gutter:    tj.Gutter,
		Roles:     tj.Roles,
	}, nil
}

// ParseMap turns a config "themes" object (name → theme object) into
// themes, ready for Build.
func ParseMap(raw map[string]json.RawMessage) (map[string]Theme, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make(map[string]Theme, len(raw))
	for name, data := range raw {
		t, err := ParseTheme(name, data)
		if err != nil {
			return nil, err
		}
		out[name] = t
	}
	return out, nil
}

// LoadDir reads every <dir>/<name>.json theme file, keyed by file name
// without the extension. A missing directory is not an error (most users
// have none); a malformed file is. Names already present in base (from the
// config "themes" object) are not overwritten by a file, so one source
// wins predictably.
func LoadDir(dir string, into map[string]Theme) (map[string]Theme, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return into, nil
		}
		return into, err
	}
	if into == nil {
		into = map[string]Theme{}
	}
	// Sorted so an error is reported deterministically.
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, fn := range names {
		name := strings.TrimSuffix(fn, ".json")
		if _, ok := into[name]; ok {
			continue // the config object already defined this name
		}
		data, err := os.ReadFile(filepath.Join(dir, fn))
		if err != nil {
			return into, err
		}
		t, err := ParseTheme(name, data)
		if err != nil {
			return into, err
		}
		into[name] = t
	}
	return into, nil
}
