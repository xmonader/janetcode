package theme

// The builtin themes. Every one fills all fourteen roles and clears the
// contrast floor TestBuiltinContrast enforces (body text ~3.5:1 against
// the terminal background the theme assumes, borders allowed lower). Add
// one here and the same test holds it to the same bar; a pretty theme
// nobody can read is worse than none.
//
// "default" is calm system colours with a blue accent; "classic" is the
// palette janetcode drew before v0.3, for anyone who preferred it. The rest are
// genuinely distinct: solarized light and dark, a high-contrast accessible
// theme, a low-saturation muted one, a near colourless mono theme built on
// ANSI indices so it inherits the user's own sixteen colours, and a set of
// well-known palettes (gruvbox, nord, catppuccin, dracula, tokyo-night,
// one-dark, rose-pine, everforest, kanagawa, matrix, monokai, github-light).

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

var builtins = []Theme{
	{
		Name:    DefaultName,
		Desc:    "calm system colours, blue accent; adapts to a light or dark terminal",
		Dark:    true,
		Builtin: true,
		Roles: map[string]Color{
			RoleAccent:       adapt("#0060c7", "#4aa3ff"),
			RoleDim:          adapt("#636366", "#98989f"),
			RoleFaint:        adapt("#6e6e73", "#7c7c82"),
			RoleWarn:         adapt("#a94f00", "#ffb340"),
			RoleErr:          adapt("#c8001a", "#ff6961"),
			RoleOK:           adapt("#1a7f37", "#4cd964"),
			RoleUser:         adapt("#0a58ba", "#8ec5ff"),
			RoleTool:         adapt("#8038a8", "#d59cf7"),
			RoleHeadBg:       adapt("#e8e8ed", "#2c2c2e"),
			RoleHeadFg:       adapt("#1d1d1f", "#f2f2f7"),
			RoleBorder:       adapt("#8e8e93", "#48484a"),
			RoleBorderActive: adapt("#0060c7", "#4aa3ff"),
			RoleDiffAdd:      adapt("#1a7f37", "#6ee08a"),
			RoleDiffDel:      adapt("#c8001a", "#ff8a80"),
		},
	},
	{
		Name:    "classic",
		Desc:    "the palette janetcode drew before v0.3; adapts to a light or dark terminal",
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
			RoleBorder:       adapt("#8b95a6", "#4b5361"),
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
			RoleBorder:       solid("#899696"), // base1, darkened to 3:1 on white
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
			RoleBorder:       adapt("#8f949e", "#565962"),
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
			RoleBorder:       adapt("#949494", "#6c6c6c"),
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
	{
		Name:    "catppuccin-mocha",
		Desc:    "catppuccin mocha: pastel blues and mauves on a cocoa ground",
		Dark:    true,
		Builtin: true,
		Roles: map[string]Color{
			RoleAccent:       solid("#89b4fa"),
			RoleDim:          solid("#bac2de"),
			RoleFaint:        solid("#a6adc8"),
			RoleWarn:         solid("#f9e2af"),
			RoleErr:          solid("#f38ba8"),
			RoleOK:           solid("#a6e3a1"),
			RoleUser:         solid("#89dceb"),
			RoleTool:         solid("#cba6f7"),
			RoleHeadBg:       solid("#313244"),
			RoleHeadFg:       solid("#cdd6f4"),
			RoleBorder:       solid("#585b70"),
			RoleBorderActive: solid("#89b4fa"),
			RoleDiffAdd:      solid("#a6e3a1"),
			RoleDiffDel:      solid("#f38ba8"),
		},
	},
	{
		Name:    "catppuccin-latte",
		Desc:    "catppuccin latte: the same pastels on warm paper",
		Dark:    false,
		Builtin: true,
		Roles: map[string]Color{
			RoleAccent:       solid("#1e66f5"),
			RoleDim:          solid("#5c5f77"),
			RoleFaint:        solid("#6c6f85"),
			RoleWarn:         solid("#bd7818"),
			RoleErr:          solid("#d20f39"),
			RoleOK:           solid("#3e9c2a"),
			RoleUser:         solid("#0392cb"),
			RoleTool:         solid("#8839ef"),
			RoleHeadBg:       solid("#e6e9ef"),
			RoleHeadFg:       solid("#4c4f69"),
			RoleBorder:       solid("#9094a2"),
			RoleBorderActive: solid("#1e66f5"),
			RoleDiffAdd:      solid("#3e9c2a"),
			RoleDiffDel:      solid("#d20f39"),
		},
	},
	{
		Name:    "dracula",
		Desc:    "dracula: cyan, pink and purple on a night-purple ground",
		Dark:    true,
		Builtin: true,
		Roles: map[string]Color{
			RoleAccent:       solid("#8be9fd"),
			RoleDim:          solid("#f8f8f2"),
			RoleFaint:        solid("#6272a4"),
			RoleWarn:         solid("#f1fa8c"),
			RoleErr:          solid("#ff5555"),
			RoleOK:           solid("#50fa7b"),
			RoleUser:         solid("#bd93f9"),
			RoleTool:         solid("#ff79c6"),
			RoleHeadBg:       solid("#44475a"),
			RoleHeadFg:       solid("#f8f8f2"),
			RoleBorder:       solid("#6272a4"),
			RoleBorderActive: solid("#bd93f9"),
			RoleDiffAdd:      solid("#50fa7b"),
			RoleDiffDel:      solid("#ff5555"),
		},
	},
	{
		Name:    "tokyo-night",
		Desc:    "tokyo night: storm-blue and neon on a midnight ground",
		Dark:    true,
		Builtin: true,
		Roles: map[string]Color{
			RoleAccent:       solid("#7aa2f7"),
			RoleDim:          solid("#c0caf5"),
			RoleFaint:        solid("#737aa2"),
			RoleWarn:         solid("#e0af68"),
			RoleErr:          solid("#f7768e"),
			RoleOK:           solid("#9ece6a"),
			RoleUser:         solid("#7dcfff"),
			RoleTool:         solid("#bb9af7"),
			RoleHeadBg:       solid("#24283b"),
			RoleHeadFg:       solid("#c0caf5"),
			RoleBorder:       solid("#565f89"),
			RoleBorderActive: solid("#7aa2f7"),
			RoleDiffAdd:      solid("#9ece6a"),
			RoleDiffDel:      solid("#f7768e"),
		},
	},
	{
		Name:    "one-dark",
		Desc:    "atom one dark: chalk pastels on a graphite ground",
		Dark:    true,
		Builtin: true,
		Roles: map[string]Color{
			RoleAccent:       solid("#61afef"),
			RoleDim:          solid("#abb2bf"),
			RoleFaint:        solid("#737a87"),
			RoleWarn:         solid("#e5c07b"),
			RoleErr:          solid("#e06c75"),
			RoleOK:           solid("#98c379"),
			RoleUser:         solid("#56b6c2"),
			RoleTool:         solid("#c678dd"),
			RoleHeadBg:       solid("#2c313c"),
			RoleHeadFg:       solid("#abb2bf"),
			RoleBorder:       solid("#4b5263"),
			RoleBorderActive: solid("#61afef"),
			RoleDiffAdd:      solid("#98c379"),
			RoleDiffDel:      solid("#e06c75"),
		},
	},
	{
		Name:    "rose-pine",
		Desc:    "rosé pine: dusty rose and pine on a dusk ground",
		Dark:    true,
		Builtin: true,
		Roles: map[string]Color{
			RoleAccent:       solid("#9ccfd8"),
			RoleDim:          solid("#e0def4"),
			RoleFaint:        solid("#6e6a86"),
			RoleWarn:         solid("#f6c177"),
			RoleErr:          solid("#eb6f92"),
			RoleOK:           solid("#3e8fb0"),
			RoleUser:         solid("#ebbcba"),
			RoleTool:         solid("#c4a7e7"),
			RoleHeadBg:       solid("#26233a"),
			RoleHeadFg:       solid("#e0def4"),
			RoleBorder:       solid("#6e6a86"),
			RoleBorderActive: solid("#c4a7e7"),
			RoleDiffAdd:      solid("#3e8fb0"),
			RoleDiffDel:      solid("#eb6f92"),
		},
	},
	{
		Name:    "everforest",
		Desc:    "everforest: moss and bark, low-glare greens on a forest ground",
		Dark:    true,
		Builtin: true,
		Roles: map[string]Color{
			RoleAccent:       solid("#7fbbb3"),
			RoleDim:          solid("#d3c6aa"),
			RoleFaint:        solid("#859289"),
			RoleWarn:         solid("#dbbc7f"),
			RoleErr:          solid("#e67e80"),
			RoleOK:           solid("#a7c080"),
			RoleUser:         solid("#83c092"),
			RoleTool:         solid("#d699b6"),
			RoleHeadBg:       solid("#3d484d"),
			RoleHeadFg:       solid("#d3c6aa"),
			RoleBorder:       solid("#859289"),
			RoleBorderActive: solid("#7fbbb3"),
			RoleDiffAdd:      solid("#a7c080"),
			RoleDiffDel:      solid("#e67e80"),
		},
	},
	{
		Name:    "kanagawa",
		Desc:    "kanagawa: ink, gold and wave-blue after the Japanese print",
		Dark:    true,
		Builtin: true,
		Roles: map[string]Color{
			RoleAccent:       solid("#7e9cd8"),
			RoleDim:          solid("#dcd7ba"),
			RoleFaint:        solid("#727169"),
			RoleWarn:         solid("#e6c384"),
			RoleErr:          solid("#ff5d62"),
			RoleOK:           solid("#98bb6c"),
			RoleUser:         solid("#7aa89f"),
			RoleTool:         solid("#957fb8"),
			RoleHeadBg:       solid("#2a2a37"),
			RoleHeadFg:       solid("#dcd7ba"),
			RoleBorder:       solid("#54546d"),
			RoleBorderActive: solid("#ffa066"),
			RoleDiffAdd:      solid("#98bb6c"),
			RoleDiffDel:      solid("#ff5d62"),
		},
	},
	{
		Name:    "matrix",
		Desc:    "phosphor rain: terminal green on a black ground",
		Dark:    true,
		Builtin: true,
		Roles: map[string]Color{
			RoleAccent:       solid("#5dff7a"),
			RoleDim:          solid("#a8e6b4"),
			RoleFaint:        solid("#7dcc8e"),
			RoleWarn:         solid("#e8d44d"),
			RoleErr:          solid("#ff6b7a"),
			RoleOK:           solid("#5dff7a"),
			RoleUser:         solid("#c8ff00"),
			RoleTool:         solid("#39ff14"),
			RoleHeadBg:       solid("#0a1f10"),
			RoleHeadFg:       solid("#c8ff00"),
			RoleBorder:       solid("#2d6b3a"),
			RoleBorderActive: solid("#5dff7a"),
			RoleDiffAdd:      solid("#5dff7a"),
			RoleDiffDel:      solid("#ff6b7a"),
		},
	},
	{
		Name:    "monokai",
		Desc:    "monokai: hot pink, lime and tangerine on a charcoal ground",
		Dark:    true,
		Builtin: true,
		Roles: map[string]Color{
			RoleAccent:       solid("#f92672"),
			RoleDim:          solid("#f8f8f2"),
			RoleFaint:        solid("#75715e"),
			RoleWarn:         solid("#e6db74"),
			RoleErr:          solid("#f92672"),
			RoleOK:           solid("#a6e22e"),
			RoleUser:         solid("#66d9ef"),
			RoleTool:         solid("#ae81ff"),
			RoleHeadBg:       solid("#3e3d32"),
			RoleHeadFg:       solid("#f8f8f2"),
			RoleBorder:       solid("#75715e"),
			RoleBorderActive: solid("#fd971f"),
			RoleDiffAdd:      solid("#a6e22e"),
			RoleDiffDel:      solid("#f92672"),
		},
	},
	{
		Name:    "github-light",
		Desc:    "github light: blue links and green/red state on white paper",
		Dark:    false,
		Builtin: true,
		Roles: map[string]Color{
			RoleAccent:       solid("#0969da"),
			RoleDim:          solid("#424a53"),
			RoleFaint:        solid("#656d76"),
			RoleWarn:         solid("#9a6700"),
			RoleErr:          solid("#cf222e"),
			RoleOK:           solid("#1a7f37"),
			RoleUser:         solid("#0550ae"),
			RoleTool:         solid("#8250df"),
			RoleHeadBg:       solid("#f6f8fa"),
			RoleHeadFg:       solid("#1f2328"),
			RoleBorder:       solid("#8c959f"), // primer gray-4; border.default #d0d7de vanishes in a terminal,
			RoleBorderActive: solid("#0969da"),
			RoleDiffAdd:      solid("#1a7f37"),
			RoleDiffDel:      solid("#cf222e"),
		},
	},
}
