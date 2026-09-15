package skill

// BuiltinSkills are skill files that ship with janetcode. A project skill
// of the same name wins. Named apart from profile.Builtins.
func BuiltinSkills() []Skill {
	return []Skill{computerUseSkill}
}

var computerUseSkill = Skill{
	Name:           "computer-use",
	Description:    "Drive the local desktop with the computer tool: screenshot, click, type, key",
	Keywords:       []string{"computer", "screenshot", "click", "desktop", "mouse", "keyboard"},
	Source:         "builtin",
	UserInvocable:  true,
	ModelInvocable: true,
	Path:           "builtin:computer-use",
	Body: `Use the computer tool to look at and operate the local desktop.

1. computer {"action":"screenshot"} writes a PNG and returns its path and size.
2. computer {"action":"click","x":N,"y":N} clicks. Coordinates are from the screenshot.
3. computer {"action":"type","text":"..."} types into the focused window.
4. computer {"action":"key","key":"Return"} presses a key (Tab, Escape, ctrl+c).
5. computer {"action":"move","x":N,"y":N} moves the pointer without clicking.

Every call is permissioned. Do not screenshot in a loop; one look, then act.
Needs xdotool (Linux) or screencapture/osascript (macOS).`,
}

// WithBuiltins returns disk skills overlaid on Builtins, project winning.
func WithBuiltins(skills []Skill) []Skill {
	byName := map[string]Skill{}
	var order []string
	add := func(s Skill) {
		if s.Name == "" {
			return
		}
		if _, ok := byName[s.Name]; !ok {
			order = append(order, s.Name)
		}
		byName[s.Name] = s
	}
	for _, s := range BuiltinSkills() {
		add(s)
	}
	for _, s := range skills {
		add(s)
	}
	out := make([]Skill, 0, len(order))
	for _, n := range order {
		out = append(out, byName[n])
	}
	return out
}
