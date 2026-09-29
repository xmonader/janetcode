package session

import (
	"fmt"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/trust"
)

// TrustEvent is the log line for a trust level being set: the project,
// the level, whether it was written down, and what it switches on or off.
func TrustEvent(st trust.State, persistent bool) Event {
	scope := "recorded"
	if !persistent {
		scope = "session"
	}
	var caps []string
	for _, c := range st.Caps {
		caps = append(caps, c.Kind+"="+c.Path)
	}
	return Event{
		Time:   time.Now(),
		Type:   TypeTrust,
		Name:   st.Level.String(),
		Reason: fmt.Sprintf("project=%s scope=%s caps=%s", st.Dir, scope, strings.Join(caps, ",")),
	}
}
