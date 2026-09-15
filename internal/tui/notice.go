package tui

import "time"

// noticeState is the one-shot line for the reserved row above the input
// (paste-chip confirmation, outside branch change). It yields to a live
// queue or steering notice and expires after noticeFor.
type noticeState struct {
	text string
	at   time.Time
}

// flash puts a one-shot line in the reserved row above the input.
// Queue, steering and a permission prompt take the row while they apply.
func (n *noticeState) flash(s string) {
	n.text, n.at = s, time.Now()
}

// live reports whether the notice is still worth a row.
func (n *noticeState) live() bool {
	return n.text != "" && time.Since(n.at) < noticeFor
}

// expire drops a notice whose time is up, reporting whether it did: the
// tick that retires one has to redraw the row it was occupying.
func (n *noticeState) expire() bool {
	if n.text != "" && time.Since(n.at) >= noticeFor {
		n.text = ""
		return true
	}
	return false
}
