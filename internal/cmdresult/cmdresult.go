// Package cmdresult holds Result, the one shape a core slash-command
// function returns and every front-end renders. The core decides what a
// command did and what to say about it; the terminal UI, the web UI and
// the CLI each have a single render helper that maps these fields onto
// their own output (a transcript note, a notice, a sheet, stdout), so a
// command's wording lives in one place instead of once per front-end.
//
// It is a leaf package (standard library only) so that any core package
// — agent, proof, eval, recipe — can return a Result without an import
// cycle.
package cmdresult

// Result is what a slash command produced, in render order: Sheet, then
// Error, then Notes, then Tips; Status and Handover are applied last.
type Result struct {
	// Sheet is a block of output (a report, a summary, a listing) shown
	// under SheetTitle. The terminal shows it verbatim, as code, unless
	// Plain is set, in which case it is shown as ordinary prose.
	Sheet      string
	SheetTitle string
	Plain      bool
	// Error is a failure to report that is part of the command's outcome
	// rather than a Go error from running it (for example a recipe step
	// the harness denied).
	Error string
	// Notes are short one-line outcomes; Tips are secondary advice.
	Notes []string
	Tips  []string
	// Status, when set, replaces the front-end's status line.
	Status string
	// Handover, when set, is a prompt the front-end submits as the next
	// turn, the same way a typed prompt starts one.
	Handover string
}
