package skill

// BuiltinSkills are skill files that ship with janetcode. A project skill
// of the same name wins. Named apart from profile.Builtins.
func BuiltinSkills() []Skill {
	return []Skill{computerUseSkill}
}

var computerUseSkill = Skill{
	Name:           "computer-use",
	Description:    "Drive the local desktop through Cua Driver (MCP) or the built-in computer tool",
	Keywords:       []string{"computer", "screenshot", "click", "desktop", "mouse", "keyboard", "cua", "accessibility", "window", "driver"},
	Source:         "builtin",
	UserInvocable:  true,
	ModelInvocable: true,
	Path:           "builtin:computer-use",
	Body: `Operate the local desktop. Prefer Cua Driver when it is connected.

# Cua Driver (preferred)

When MCP tools named mcp__cua-driver__* exist (tool_search finds them), use those. They talk to the native CUA driver: accessibility trees, window ids, element tokens, and screenshots.

Workflow, every turn:
1. Discover: mcp__cua-driver__list_apps, then list_windows (or get_app_state on open-computer-use).
2. Observe: get_window_state / get_app_state for the target window. Ground on BOTH the element tree and the screenshot. Note pid, window_id, snapshot_id / element_token.
3. Act once: click, type_text, press_key, hotkey, scroll. Prefer element_token or element_index from that snapshot. Pixel x,y only for canvases / WebGL / surfaces missing from the tree.
4. Re-observe before the next action. Indices do not survive a new snapshot.
5. Escalate delivery_mode to "foreground" only when a background action did not land.

Do not dispatch arbitrary driver tool names you have not listed. Do not screenshot in a tight loop.

If those MCP tools are missing, tell the user to run /cua on (needs cua-driver or open-computer-use on PATH). Do not invent a driver.

# Built-in fallback

The computer tool is a last-resort pixel driver (screenshot writes a PNG; click/type/key via xdotool or osascript). Use it only when Cua Driver is not connected. Coordinates are from that PNG. Every call is permissioned.

Needs xdotool + grim/import/scrot (Linux) or screencapture/osascript (macOS).`,
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
