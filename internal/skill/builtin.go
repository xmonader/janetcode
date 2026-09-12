package skill

// Builtins are subagent profiles that ship with janetcode. They are
// selectable with task agent=<name> without a file in .janetcode/agents.
func Builtins() []Profile {
	return []Profile{exploreProfile, generalProfile}
}

var exploreProfile = Profile{
	Name:        "explore",
	Description: "read-only exploration of the codebase: map, search, and report; cannot edit or run commands",
	Source:      "builtin",
	Body: `You are a read-only explore subagent. Your job is to look through the codebase and report what you find: relevant paths with line numbers, how something is wired, and anything surprising.

You cannot edit files, write files, or run shell commands. Use read, list, glob, grep and any other read-only tools you have. Do not ask questions; make reasonable assumptions and state them. Return a complete, self-contained report.`,
}

var generalProfile = Profile{
	Name:        "general",
	Description: "general-purpose teammate: investigates and reports; cannot edit or run commands",
	Source:      "builtin",
	Body: `You are a teammate of the main agent. Do the job in the brief completely and report back.

You cannot edit files, write files, or run shell commands unless you were started with edit:true. Use the tools you have. Do not ask questions; make reasonable assumptions and state them. Return a complete, self-contained report.`,
}

// MergeProfiles returns base then overlay, with overlay winning on the
// same name so a project explore.md can replace the builtin.
func MergeProfiles(base, overlay []Profile) []Profile {
	byName := map[string]Profile{}
	var order []string
	add := func(p Profile) {
		if p.Name == "" {
			return
		}
		if _, ok := byName[p.Name]; !ok {
			order = append(order, p.Name)
		}
		byName[p.Name] = p
	}
	for _, p := range base {
		add(p)
	}
	for _, p := range overlay {
		add(p)
	}
	out := make([]Profile, 0, len(order))
	for _, n := range order {
		out = append(out, byName[n])
	}
	return out
}
