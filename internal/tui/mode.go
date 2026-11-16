package tui

// The one answer to "what owns the keyboard right now".
//
// The window used to answer that question five times over, once per
// caller, each by re-testing the same set of pointers: handleKey, View,
// completions, planHeight and the hint engine. They did not agree —
// three of the five left m.wiz out — so while the /mcp add dialog was up
// the completion menu was still being computed and reserved footer rows
// for, the plan strip still claimed height the modal's layout did not
// give it, and the hint engine thought the user was idle and free to be
// interrupted. Every new modal had to remember to appear in all five.
//
// mode() is now the only place that decision is made, and everything
// else asks it. A modal that forgets to add itself here is visible as
// one missing case rather than as four subtly different behaviours.

// uiMode says which part of the window is reading keys. It is derived,
// not stored: the modal state itself lives in the fields mode() reads,
// so the two cannot drift apart the way two stored booleans can.
type uiMode uint8

const (
	// modePrompt is the ordinary composer: typing, completions, history,
	// and the prompt-key table. A turn may well be running — that is an
	// orthogonal fact (turnState.running), because a running turn still
	// leaves the keyboard with the composer so the user can queue, steer
	// or cancel.
	modePrompt uiMode = iota
	// modeAsk is the model asking the user a question (the ask tool).
	modeAsk
	// modeSecret is the private prompt for a secret (request_secret).
	modeSecret
	// modeOverlay is a live panel drawn over the transcript.
	modeOverlay
	// modePicker is the searchable list.
	modePicker
	// modeConnect is the provider/model dialog.
	modeConnect
	// modeWizard is a step dialog (/mcp add).
	modeWizard
	// modeApprove is a permission prompt: it takes the whole keyboard.
	modeApprove
	// modeDeny is a permission prompt collecting the optional reason for
	// a denial; the composer takes text again, but the prompt is still up
	// and still owns enter and esc.
	modeDeny
	// modeSearch is the reverse history search (ctrl+r): typed text is
	// the query, and the match is shown in the composer.
	modeSearch
)

func (k uiMode) String() string {
	switch k {
	case modePrompt:
		return "prompt"
	case modeAsk:
		return "ask"
	case modeSecret:
		return "secret"
	case modeOverlay:
		return "overlay"
	case modePicker:
		return "picker"
	case modeConnect:
		return "connect"
	case modeWizard:
		return "wizard"
	case modeApprove:
		return "approve"
	case modeDeny:
		return "deny"
	case modeSearch:
		return "search"
	}
	return "unknown"
}

// mode reports what owns the keyboard, in the priority handleKey routes
// on. The order matters where two can be up at once: a permission prompt
// raised while a picker is open is answered by the picker's keys until
// the picker closes, which is what the old switch did and what the
// interface has always looked like. The picker likewise outranks the
// overlays: /trajectory's search is a picker over the ledger, and the
// innermost modal takes the keys.
func (m *model) mode() uiMode {
	switch {
	case m.secreting != nil:
		return modeSecret
	case m.asking != nil:
		return modeAsk
	case m.picker != nil:
		return modePicker
	case m.ov.kind != "":
		return modeOverlay
	case m.connect != nil:
		return modeConnect
	case m.wiz != nil:
		return modeWizard
	case m.pending != nil && m.denying:
		return modeDeny
	case m.pending != nil:
		return modeApprove
	case m.hsearch != nil:
		return modeSearch
	}
	return modePrompt
}

// modal reports whether a dialog has replaced the transcript, input and
// footer with its own body. The permission prompt is not one: it is
// drawn into the ordinary window, over the composer.
func (k uiMode) modal() bool {
	switch k {
	case modeAsk, modeSecret, modeOverlay, modePicker, modeConnect, modeWizard:
		return true
	}
	return false
}

// blocked reports whether the user is in the middle of answering
// something. Nothing may interrupt them there — no hint, no completion
// menu, no plan strip taking a row out from under the dialog.
func (k uiMode) blocked() bool { return k != modePrompt }
