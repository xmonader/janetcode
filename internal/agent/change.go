package agent

import "github.com/xmonader/janetcode/internal/change"

// The provenance record's own types live in internal/change: they are
// data and arithmetic over it, and internal/undoabout needs them without
// needing an agent. These aliases keep the names the front-ends, whatif
// and the session's undo file already use.

// Change is one file changed by one tool call; see change.Change.
type Change = change.Change

// Hunk is one contiguous run of changed lines; see change.Hunk.
type Hunk = change.Hunk

// ErrNoSuchChange is returned for an unknown change number.
var ErrNoSuchChange = change.ErrNoSuchChange

// ErrNoChange is returned by Blame when this session never changed the
// path, so a caller can fall back to committed history (see
// internal/proof.BlameCommitted) instead of matching the message text.
var ErrNoChange = change.ErrNoChange
