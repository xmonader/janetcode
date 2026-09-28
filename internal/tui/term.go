package tui

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// The terminal can take part in the session if it is told what is going
// on. Three things are worth telling it:
//
//   - OSC 133 marks where a prompt starts (A), where the user's text ends
//     and the work begins (B), where the model's answer starts (C) and
//     where the turn finished (D, with an exit code). Terminals that
//     understand it give the user prev/next-prompt keys that jump between
//     turns and a mark in the scrollbar for a failed one.
//   - OSC 9 (and the kitty and urxvt variants) raises a desktop
//     notification. It is only ever sent while the window is unfocused,
//     which the terminal reports through DEC 1004 focus events.
//   - OSC 0/2 sets the window and tab title, so a row of terminals says
//     which session is in which. bubbletea writes that one for us.
//
// Everything here is silent when notifications are turned off
// (JANETCODE_NOTIFY=0) or the output is not a terminal.
type termIntegration struct {
	out io.Writer
	// focused tracks DEC 1004 focus reports. A terminal that never sends
	// them leaves this true, and nothing is ever announced — better than
	// a notification for every turn on the window you are looking at.
	focused bool
	// notify enables desktop notifications and the bell.
	notify bool
	// brand picks the notification escape the terminal understands.
	brand string
}

func newTermIntegration() *termIntegration {
	return &termIntegration{out: os.Stdout, focused: true, notify: notifyEnabled(os.Getenv), brand: termBrand(os.Getenv)}
}

// notifyEnabled reads the switch that turns desktop notifications off.
func notifyEnabled(env func(string) string) bool {
	switch strings.ToLower(strings.TrimSpace(env("JANETCODE_NOTIFY"))) {
	case "0", "off", "false", "no", "never":
		return false
	}
	return true
}

// termBrand names the notification dialect the terminal speaks. Only the
// ones that need something other than OSC 9 are listed.
func termBrand(env func(string) string) string {
	switch {
	case env("KITTY_WINDOW_ID") != "" || strings.Contains(env("TERM"), "kitty"):
		return "kitty"
	case strings.Contains(strings.ToLower(env("TERM_PROGRAM")), "iterm"):
		return "iterm2"
	case strings.HasPrefix(env("TERM"), "rxvt"):
		return "rxvt"
	}
	return ""
}

// mark writes one OSC 133 zone marker. The turn's shape in the terminal's
// eyes is A B … C … D;code.
func (t *termIntegration) mark(m string) {
	if t == nil || t.out == nil {
		return
	}
	fmt.Fprintf(t.out, "\x1b]133;%s\x07", m)
}

// turnStarted marks the end of the prompt and the start of the work.
func (t *termIntegration) turnStarted() {
	t.mark("A")
	t.mark("B")
	t.mark("C")
}

// turnEnded closes the zone with an exit code and, when the window is not
// the one being looked at, raises a notification.
func (t *termIntegration) turnEnded(ok bool, summary string) {
	code := 1
	if ok {
		code = 0
	}
	t.mark(fmt.Sprintf("D;%d", code))
	t.announce("janetcode", summary)
}

// announce raises a desktop notification, but only while the terminal
// reports itself unfocused: an alert about the window in front of you is
// noise. A terminal that answers none of the escapes still gets the bell.
func (t *termIntegration) announce(title, body string) {
	if t == nil || t.out == nil || !t.notify || t.focused {
		return
	}
	body = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, body)
	switch t.brand {
	case "kitty":
		fmt.Fprintf(t.out, "\x1b]99;i=1:d=0;%s\x07", body)
	case "rxvt":
		fmt.Fprintf(t.out, "\x1b]777;notify;%s;%s\x07", title, body)
	default:
		fmt.Fprintf(t.out, "\x1b]9;%s\x07", body)
		io.WriteString(t.out, "\a") // the fallback every terminal has
	}
}

// windowTitle names this session for the terminal's tab: the session
// title once one has been generated, the working directory before that.
func (m *model) windowTitle() string {
	name := ""
	if m.opts.Session != nil {
		name = m.opts.Session.Meta.Title
	}
	if name == "" {
		name = filepath.Base(m.opts.Cwd)
	}
	// The title rides inside an OSC sequence of its own: a BEL or an ESC
	// in a generated title would end it early and send the rest to the
	// terminal as a sequence of its own choosing.
	return "janetcode · " + sanitize(name)
}
