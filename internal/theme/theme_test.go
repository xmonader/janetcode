package theme

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// bodyFloor is the contrast a role's text is held to against the terminal
// background the theme assumes. It matches the ~3.5:1 the styles.go
// comment names for body text; faint (the quietest text) is allowed a
// touch lower, borders are not text and are not checked here.
const (
	bodyFloor   = 3.5
	faintFloor  = 3.0
	diffFloor   = 3.0
	headFloor   = 4.0
	borderFloor = 3.0
)

// textRoles are checked against the terminal background; borders are not.
var textRoles = map[string]float64{
	RoleAccent: bodyFloor,
	RoleDim:    bodyFloor,
	RoleFaint:  faintFloor,
	RoleWarn:   bodyFloor,
	RoleErr:    bodyFloor,
	RoleOK:     bodyFloor,
	RoleUser:   bodyFloor,
	RoleTool:   bodyFloor,
}

func TestBuiltinContrast(t *testing.T) {
	for _, th := range Builtins() {
		modes := []bool{th.Dark}
		if th.Adaptive() {
			modes = []bool{false, true}
		}
		for _, dark := range modes {
			bg := AssumedBg(dark)
			modeName := map[bool]string{true: "dark", false: "light"}[dark]
			for role, floor := range textRoles {
				fg := th.Get(role).For(dark)
				ratio, ok := Contrast(fg, bg)
				if !ok {
					continue // an ANSI index defers to the terminal
				}
				if ratio < floor {
					t.Errorf("%s (%s): role %s %s on %s is %.2f:1, want ≥ %.1f",
						th.Name, modeName, role, fg, bg, ratio, floor)
				}
			}
			// The header text sits on the header background, not the
			// terminal's, so it is judged against that.
			hfg := th.Get(RoleHeadFg).For(dark)
			hbg := th.Get(RoleHeadBg).For(dark)
			if ratio, ok := Contrast(hfg, hbg); ok && ratio < headFloor {
				t.Errorf("%s (%s): header %s on %s is %.2f:1, want ≥ %.1f", th.Name, modeName, hfg, hbg, ratio, headFloor)
			}
			// Borders are not text, but a box edge that fades into a
			// light terminal leaves the dialog floating: WCAG's 3:1
			// for UI components, checked where the theme picks the
			// light background.
			if !dark {
				fg := th.Get(RoleBorder).For(dark)
				if ratio, ok := Contrast(fg, bg); ok && ratio < borderFloor {
					t.Errorf("%s (light): border %s on %s is %.2f:1, want ≥ %.1f", th.Name, fg, bg, ratio, borderFloor)
				}
			}
			for _, role := range []string{RoleDiffAdd, RoleDiffDel} {
				fg := th.Get(role).For(dark)
				if ratio, ok := Contrast(fg, bg); ok && ratio < diffFloor {
					t.Errorf("%s (%s): role %s %s on %s is %.2f:1, want ≥ %.1f", th.Name, modeName, role, fg, bg, ratio, diffFloor)
				}
			}
		}
	}
}

// TestContrastReport prints every ratio so a theme can be tuned; it never
// fails. Run with -v.
func TestContrastReport(t *testing.T) {
	for _, th := range Builtins() {
		modes := []bool{th.Dark}
		if th.Adaptive() {
			modes = []bool{false, true}
		}
		for _, dark := range modes {
			bg := AssumedBg(dark)
			var sb strings.Builder
			fmt.Fprintf(&sb, "%s (%v on %s):", th.Name, map[bool]string{true: "dark", false: "light"}[dark], bg)
			for _, role := range Roles {
				fg := th.Get(role).For(dark)
				if r, ok := Contrast(fg, bg); ok {
					fmt.Fprintf(&sb, " %s=%.1f", role, r)
				} else {
					fmt.Fprintf(&sb, " %s=ansi", role)
				}
			}
			t.Log(sb.String())
		}
	}
}

func TestBuiltinsAreComplete(t *testing.T) {
	set, err := Build(nil)
	if err != nil {
		t.Fatalf("Build(nil): %v", err)
	}
	if !set.Has(DefaultName) {
		t.Fatalf("no %q theme", DefaultName)
	}
	for _, th := range set.All() {
		if err := requireComplete(th); err != nil {
			t.Errorf("%v", err)
		}
	}
	if len(set.Names()) < 16 {
		t.Errorf("want at least 16 builtins, got %d", len(set.Names()))
	}
}

func TestUserThemeValidation(t *testing.T) {
	cases := []struct {
		name    string
		themes  map[string]Theme
		wantErr string
	}{
		{"unknown role", map[string]Theme{"x": {Base: DefaultName, Roles: map[string]Color{"nope": solid("#fff")}}}, "unknown role"},
		{"bad colour", map[string]Theme{"x": {Base: DefaultName, Roles: map[string]Color{RoleAccent: solid("blue")}}}, "want a #rrggbb"},
		{"short hex ok", map[string]Theme{"x": {Base: DefaultName, Roles: map[string]Color{RoleAccent: solid("#08f")}}}, ""},
		{"missing role, no base", map[string]Theme{"x": {Roles: map[string]Color{RoleAccent: solid("#08f")}}}, "missing role"},
		{"unknown base", map[string]Theme{"x": {Base: "ghost", Roles: map[string]Color{RoleAccent: solid("#08f")}}}, "not a known theme"},
		{"complete via base", map[string]Theme{"x": {Base: "default", Roles: map[string]Color{RoleAccent: solid("#08f")}}}, ""},
		{"override builtin", map[string]Theme{"gruvbox": {Base: "default", Roles: map[string]Color{RoleAccent: solid("#08f")}}}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Build(c.themes)
			switch {
			case c.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case c.wantErr != "" && err == nil:
				t.Fatalf("want error containing %q, got nil", c.wantErr)
			case c.wantErr != "" && !strings.Contains(err.Error(), c.wantErr):
				t.Fatalf("want error containing %q, got %v", c.wantErr, err)
			}
		})
	}
}

// TestOneSidedColourInheritsTheOtherSide: a role written as {"dark": …}
// alone was documented to keep the base's light colour, but the parser
// copied the dark colour into the light side before the base was
// consulted, so the default's light-mode accent became the dark-mode one.
// Without a base the one side still stands for both.
func TestOneSidedColourInheritsTheOtherSide(t *testing.T) {
	themes, err := ParseMap(rawThemes{
		"withbase": []byte(`{"base":"default","roles":{"accent":{"dark":"#010203"}}}`),
	}.raw())
	if err != nil {
		t.Fatal(err)
	}
	set, err := Build(themes)
	if err != nil {
		t.Fatal(err)
	}
	th, _ := set.Get("withbase")
	def := set.Default().Get(RoleAccent)
	if got := th.Get(RoleAccent); got.Dark != "#010203" || got.Light != def.Light {
		t.Fatalf("accent = %+v, want dark #010203 and the default's light %s", got, def.Light)
	}

	// A base-less theme: the one side given is used for both.
	roles := `"accent":{"light":"#111111"}`
	for _, r := range Roles {
		if r != RoleAccent {
			roles += fmt.Sprintf(`,%q:"#222222"`, r)
		}
	}
	lone, err := ParseTheme("lone", []byte(`{"roles":{`+roles+`}}`))
	if err != nil {
		t.Fatal(err)
	}
	set, err = Build(map[string]Theme{"lone": lone})
	if err != nil {
		t.Fatal(err)
	}
	th, _ = set.Get("lone")
	if got := th.Get(RoleAccent); got.Light != "#111111" || got.Dark != "#111111" {
		t.Fatalf("base-less one-sided accent = %+v, want #111111 on both sides", got)
	}
}

type rawThemes map[string][]byte

func (r rawThemes) raw() map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	for k, v := range r {
		out[k] = v
	}
	return out
}

func TestUserThemeOverridesBuiltin(t *testing.T) {
	set, err := Build(map[string]Theme{
		"gruvbox": {Desc: "mine", Dark: true, Base: DefaultName, Roles: map[string]Color{RoleAccent: solid("#010203")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	th, _ := set.Get("gruvbox")
	if th.Desc != "mine" || th.Get(RoleAccent).Dark != "#010203" {
		t.Fatalf("user theme did not override builtin: %+v", th)
	}
	if th.Builtin {
		t.Errorf("overridden theme should not be marked builtin")
	}
}
