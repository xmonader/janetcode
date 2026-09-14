package agent

import (
	"fmt"
	"strings"
)

// ParseSwitches reads a comma-separated list of behaviour names to turn
// off (--eval-off, --off, JANETCODE_OFF) into the map Agent.Off takes.
// Blank entries are skipped. An unknown name is an error listing every
// known one; the returned map still holds the names that were valid, for
// a caller that only wants to warn.
func ParseSwitches(csv string) (map[string]bool, error) {
	off := map[string]bool{}
	var unknown []string
	for _, name := range strings.Split(csv, ",") {
		if name = strings.TrimSpace(name); name == "" {
			continue
		}
		if !KnownSwitch(name) {
			unknown = append(unknown, name)
			continue
		}
		off[name] = true
	}
	if len(unknown) > 0 {
		return off, fmt.Errorf("unknown behaviour %q (%s)", unknown[0], strings.Join(Switches(), ", "))
	}
	return off, nil
}
