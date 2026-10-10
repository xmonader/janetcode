package agent

import (
	"context"
	"time"

	"github.com/xmonader/janetcode/internal/provenance"
)

// fingerprintTimeout bounds the session-start canaries: they are six
// one-word answers, and a route too slow to give them in this long will
// say so on the first real call anyway.
const fingerprintTimeout = 90 * time.Second

// fingerprintSession runs the behavioural fingerprint once per session,
// at the start of its first turn (internal/provenance, Take/Session). A
// route that answers like another model is reported as a notice, which
// the session log and a headless run's output carry; the check never
// stops the turn, and a route that cannot answer the canaries is noted
// and left alone.
func (a *Agent) fingerprintSession(ctx context.Context, emit func(Event)) {
	if a.FingerprintDir == "" || a.fingerprintDone.Swap(true) {
		return
	}
	s := a.Settings()
	if s.Provider == nil || s.Model == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, fingerprintTimeout)
	defer cancel()
	route := s.Provider.Name() + "/" + s.Model
	notice, _, err := provenance.FingerprintStore{Dir: a.FingerprintDir}.Session(ctx, s.Provider, route, s.Model)
	switch {
	case err != nil:
		emit(Event{Kind: EvNotice, Text: "fingerprint: not taken (" + err.Error() + ")"})
	case notice != "":
		emit(Event{Kind: EvNotice, Text: notice})
	}
}
