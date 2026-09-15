package skill

import "github.com/xmonader/janetcode/internal/skill/profile"

// Profile is a custom subagent definition; see profile.Profile.
type Profile = profile.Profile

// Builtins are the subagent profiles that ship with janetcode.
func Builtins() []Profile { return profile.Builtins() }

// MergeProfiles returns base then overlay, overlay winning on a name.
func MergeProfiles(base, overlay []Profile) []Profile { return profile.MergeProfiles(base, overlay) }

// LoadProfiles reads every profile visible from cwd.
func LoadProfiles(cwd, configDir string) []Profile { return profile.LoadProfiles(cwd, configDir) }
