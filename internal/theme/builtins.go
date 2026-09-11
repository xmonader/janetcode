package theme

// The builtin themes. Every one fills all fourteen roles and clears the
// contrast floor TestBuiltinContrast enforces (body text ~3.5:1 against
// the terminal background the theme assumes, borders allowed lower). Add
// one here and the same test holds it to the same bar; a pretty theme
// nobody can read is worse than none.
//
// "default" is the palette janetcode has always drawn, kept exactly so an
// upgrade changes nothing for anyone who never picks a theme. The rest are
// genuinely distinct: two solarized-derived pairs light and dark, a
// high-contrast accessible theme, a low-saturation muted one, a near
// colourless mono theme built on ANSI indices so it inherits the user's
// own sixteen colours, and two with character (a warm gruvbox and a cool
// nord).

// Builtins returns a fresh copy of every shipped theme.
func Builtins() []Theme {
	out := make([]Theme, len(builtins))
	copy(out, builtins)
	for i := range out {
		out[i].Roles = cloneRoles(out[i].Roles)
	}
	return out
}

// builtin returns a shipped theme by name, or a zero Theme.
func builtin(name string) Theme {
	for _, t := range builtins {
		if t.Name == name {
			t.Roles = cloneRoles(t.Roles)
			return t
		}
	}
	return Theme{}
}

func gutter(n int) *int { return &n }

var builtins = []Theme{
	{
		Name:    DefaultName,
		Desc:    "the palette janetcode has always drawn; adapts to a light or dark terminal",
		Dark:    true,
		Builtin: true,
		Roles: map[string]Color{
			RoleAccent:       adapt("#0b63c6", "#7cc4ff"),
			RoleDim:          adapt("#6b7280", "#8b93a1"),
			RoleFaint:        adapt("#7c8494", "#6b7484"),
			RoleWarn:         adapt("#b45309", "#f5b642"),
			RoleErr:          adapt("#b91c1c", "#ff6b6b"),
			RoleOK:           adapt("#15803d", "#5fd38d"),
			RoleUser:         adapt("#1d4ed8", "#9ad0ff"),
			RoleTool:         adapt("#6d28d9", "#c4a7ff"),
			RoleHeadBg:       adapt("#e5e7eb", "#2b2f36"),
			RoleHeadFg:       adapt("#111827", "#e6e9ef"),
			RoleBorder:       adapt("#96a1b3", "#4b5361"),
			RoleBorderActive: adapt("#0b63c6", "#7cc4ff"),
			RoleDiffAdd:      adapt("#166534", "#7ee2a8"),
			RoleDiffDel:      adapt("#991b1b", "#ff8a8a"),
		},
	},
	{
		Name:    "solarized-dark",
		Desc:    "Ethan Schoonover's solarized, warm and low-glare, for a dark terminal",
		Dark:    true,
		Builtin: true,
		Roles: map[string]Color{
			RoleAccent:       solid("#4fb3e8"), // blue, brightened to clear the floor
			RoleDim:          solid("#93a1a1"), // base1
			RoleFaint:        solid("#839496"), // base0
			RoleWarn:         solid("#d5a021"), // yellow
			RoleErr:          solid("#f4553f"), // red
			RoleOK:           solid("#98b326"), // green
			RoleUser:         solid("#7cc4ff"),
			RoleTool:         solid("#c095d6"), // violet/magenta
			RoleHeadBg:       solid("#073642"), // base02
			RoleHeadFg:       solid("#eee8d5"), // base2
			RoleBorder:       solid("#586e75"), // base01
			RoleBorderActive: solid("#4fb3e8"),
			RoleDiffAdd:      solid("#98b326"),
			RoleDiffDel:      solid("#f4553f"),
		},
	},
	{
		Name:    "solarized-light",
		Desc:    "solarized on a light terminal: muted ink on paper",
		Dark:    false,
		Builtin: true,
		Roles: map[string]Color{
			RoleAccent:       solid("#1c72b8"),
			RoleDim:          solid("#586e75"), // base01
			RoleFaint:        solid("#657b83"), // base00
			RoleWarn:         solid("#a5670a"),
			RoleErr:          solid("#c22b26"),
			RoleOK:           solid("#5c7300"),
			RoleUser:         solid("#1c5fb8"),
			RoleTool:         solid("#8250a8"),
			RoleHeadBg:       solid("#eee8d5"), // base2
			RoleHeadFg:       solid("#073642"), // base02
			RoleBorder:       solid("#93a1a1"), // base1
			RoleBorderActive: solid("#1c72b8"),
			RoleDiffAdd:      solid("#4f6600"),
			RoleDiffDel:      solid("#c22b26"),
		},
	},
	{
		Name:    "high-contrast",
		Desc:    "accessible: bright, saturated foregrounds on a dark ground, well past the contrast floor",
		Dark:    true,
		Builtin: true,
		Roles: map[string]Color{
			RoleAccent:       solid("#5eb1ff"),
			RoleDim:          solid("#d0d0d0"),
			RoleFaint:        solid("#a8a8a8"),
			RoleWarn:         solid("#ffcf3f"),
			RoleErr:          solid("#ff5f5f"),
			RoleOK:           solid("#5fff87"),
			RoleUser:         solid("#87d7ff"),
			RoleTool:         solid("#d7a7ff"),
			RoleHeadBg:       solid("#000000"),
			RoleHeadFg:       solid("#ffffff"),
			RoleBorder:       solid("#8a8a8a"),
			RoleBorderActive: solid("#5eb1ff"),
			RoleDiffAdd:      solid("#87ffaf"),
			RoleDiffDel:      solid("#ff8787"),
		},
	},
	{
		Name:    "muted",
		Desc:    "low-saturation: greyed foregrounds with just enough tint to tell roles apart; adapts",
		Dark:    true,
		Builtin: true,
		Roles: map[string]Color{
			RoleAccent:       adapt("#3a6ea5", "#8fb3d9"),
			RoleDim:          adapt("#5f6672", "#9aa0ab"),
			RoleFaint:        adapt("#767c88", "#7f8590"),
			RoleWarn:         adapt("#8a6a2f", "#c8a96a"),
			RoleErr:          adapt("#9d4a4a", "#d99a9a"),
			RoleOK:           adapt("#4a7a55", "#9ac8a6"),
			RoleUser:         adapt("#4a6a9a", "#a6bcd9"),
			RoleTool:         adapt("#6a5a8a", "#b6a9cf"),
			RoleHeadBg:       adapt("#e0e0e2", "#33353a"),
			RoleHeadFg:       adapt("#2a2c30", "#d7d9dd"),
			RoleBorder:       adapt("#9aa0aa", "#565962"),
			RoleBorderActive: adapt("#3a6ea5", "#8fb3d9"),
			RoleDiffAdd:      adapt("#4a7a55", "#9ac8a6"),
			RoleDiffDel:      adapt("#9d4a4a", "#d99a9a"),
		},
	},
	{
		Name: "mono",
		Desc: "near colourless: greys plus the terminal's own red/green/blue for state, built on ANSI indices",
		Dark: true,
		// The state roles are ANSI indices so they are whatever the user's
		// terminal renders for red/green/yellow/blue — the same reasoning as
		// the ANSI-16 fallback: never guess a colour the terminal already
		// owns. The greys are hex so the quiet text is genuinely quiet.
		Builtin: true,
		Roles: map[string]Color{
			RoleAccent:       adapt("#303030", "#e4e4e4"),
			RoleDim:          adapt("#585858", "#b2b2b2"),
			RoleFaint:        adapt("#767676", "#949494"),
			RoleWarn:         adapt("3", "11"),
			RoleErr:          adapt("1", "9"),
			RoleOK:           adapt("2", "10"),
			RoleUser:         adapt("#1c1c1c", "#ffffff"),
			RoleTool:         adapt("#444444", "#c6c6c6"),
			RoleHeadBg:       adapt("#dadada", "#303030"),
			RoleHeadFg:       adapt("#1c1c1c", "#eeeeee"),
			RoleBorder:       adapt("#9e9e9e", "#6c6c6c"),
			RoleBorderActive: adapt("#303030", "#e4e4e4"),
			RoleDiffAdd:      adapt("2", "10"),
			RoleDiffDel:      adapt("1", "9"),
		},
	},
	{
		Name:    "gruvbox",
		Desc:    "warm retro: gruvbox's amber and olive on a soft dark ground",
		Dark:    true,
		Builtin: true,
		Roles: map[string]Color{
			RoleAccent:       solid("#83a598"), // blue
			RoleDim:          solid("#d5c4a1"), // fg2
			RoleFaint:        solid("#a89984"), // gray
			RoleWarn:         solid("#fabd2f"), // yellow
			RoleErr:          solid("#fb4934"), // red
			RoleOK:           solid("#b8bb26"), // green
			RoleUser:         solid("#8ec07c"), // aqua
			RoleTool:         solid("#d3869b"), // purple
			RoleHeadBg:       solid("#3c3836"),
			RoleHeadFg:       solid("#fbf1c7"),
			RoleBorder:       solid("#665c54"),
			RoleBorderActive: solid("#fe8019"), // orange
			RoleDiffAdd:      solid("#b8bb26"),
			RoleDiffDel:      solid("#fb4934"),
		},
	},
	{
		Name:    "nord",
		Desc:    "cool arctic: nord's blues and frost on a slate ground",
		Dark:    true,
		Builtin: true,
		Roles: map[string]Color{
			RoleAccent:       solid("#88c0d0"), // frost
			RoleDim:          solid("#d8dee9"), // snow
			RoleFaint:        solid("#9aa5b8"),
			RoleWarn:         solid("#ebcb8b"), // aurora yellow
			RoleErr:          solid("#e88b93"), // aurora red, lightened
			RoleOK:           solid("#a3be8c"), // aurora green
			RoleUser:         solid("#81a1c1"),
			RoleTool:         solid("#b48ead"), // aurora purple
			RoleHeadBg:       solid("#3b4252"),
			RoleHeadFg:       solid("#eceff4"),
			RoleBorder:       solid("#4c566a"),
			RoleBorderActive: solid("#88c0d0"),
			RoleDiffAdd:      solid("#a3be8c"),
			RoleDiffDel:      solid("#e88b93"),
		},
	},
}
