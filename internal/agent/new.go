package agent

import (
	"errors"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

// Deps are the collaborators an Agent cannot run without, plus the
// session log it writes to.
type Deps struct {
	// Provider answers the model calls. It may be nil only until a
	// front-end connects one (see SwitchModel): a session started with no
	// credentials is built that way and cannot run a turn before /connect.
	Provider llm.Provider
	// Tools is the tool registry the model may call. Required.
	Tools *tool.Registry
	// Log is the session's trajectory; nil keeps stats in memory only.
	Log session.Log
}

// New builds an Agent from its collaborators and its initial settings.
// The result is exactly the Agent a struct literal with the same fields
// would be (a zero setting keeps meaning "the default" wherever it is
// read); the caller sets anything else — System, Root, Archive, Messages
// and so on — before the first turn. s.Provider must be left nil: the
// provider comes from deps.
func New(deps Deps, s Settings) (*Agent, error) {
	switch {
	case deps.Tools == nil:
		return nil, errors.New("agent: no tool registry")
	case s.Provider != nil:
		return nil, errors.New("agent: the provider goes in Deps, not Settings")
	}
	return &Agent{
		Provider:      deps.Provider,
		Tools:         deps.Tools,
		Log:           deps.Log,
		Model:         s.Model,
		Price:         s.Price,
		ContextWindow: s.ContextWindow,
		MaxTokens:     s.MaxTokens,
		MaxIterations: s.MaxIterations,
		CompactAt:     s.CompactAt,
		AutoApprove:   s.AutoApprove,
		AutoEdits:     s.AutoEdits,
		AutoMode:      s.AutoMode,
		ReadOnly:      s.ReadOnly,
		PlanMode:      s.PlanMode,
		ProofNotes:    s.ProofNotes,
	}, nil
}
