package tui

import "github.com/xmonader/janetcode/internal/cmdresult"

// showResult renders what a core command function returned: err as a
// transcript error, otherwise the Result's sheet (verbatim, or as prose
// when Plain), error, notes (the status line), tips and status. A
// Result's Handover is left to the caller, which decides when to submit
// it relative to ending its own work.
func (m *model) showResult(res cmdresult.Result, err error) {
	if err != nil {
		m.transcript.errorf("%v", err)
		return
	}
	if res.Sheet != "" {
		if res.Plain {
			m.transcript.info(res.Sheet)
		} else {
			m.transcript.code(res.Sheet)
		}
	}
	if res.Error != "" {
		m.transcript.errorf("%s", res.Error)
	}
	for _, n := range res.Notes {
		m.note(n)
	}
	for _, t := range res.Tips {
		m.transcript.tip(t)
	}
	if res.Status != "" {
		m.status = res.Status
	}
}
