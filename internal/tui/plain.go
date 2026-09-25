package tui

import (
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
)

// Plain mode is for a terminal that cannot draw the interface's glyphs —
// the Linux console, a serial line, a font without box drawing — and for
// anyone who wants a quieter screen: every glyph the interface draws is
// swapped for one ASCII character, and the spinner stands still. One
// character for one keeps every width the layout computed, so borders
// still meet and columns still line up.
//
// It is --plain or JANETCODE_PLAIN=1, and it is on by itself when TERM
// is "linux", whose console font has none of these.

// PlainEnv turns plain mode on.
const PlainEnv = "JANETCODE_PLAIN"

// PlainWanted reports whether the environment asks for plain mode.
func PlainWanted(env func(string) string) bool {
	switch strings.ToLower(strings.TrimSpace(env(PlainEnv))) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return env("TERM") == "linux"
}

// plainGlyphs maps each non-ASCII glyph the interface draws to the ASCII
// character that stands in for it. Every key is one terminal column wide.
var plainGlyphs = map[rune]rune{
	// borders
	'╭': '+', '╮': '+', '╰': '+', '╯': '+', '┌': '+', '┐': '+', '└': '+', '┘': '+',
	'├': '+', '┤': '+', '┬': '+', '┴': '+', '┼': '+', '─': '-', '╌': '-', '│': '|',
	// marks and bullets
	'◆': '*', '✦': '*', '●': '*', '•': '*', '◎': 'o', '✓': '+', '✗': 'x', '×': 'x', '⚠': '!',
	'⊘': '-', '⚑': '!', '⚙': '*', '⧉': '#', '❯': '>', '›': '>', '»': '>',
	'▸': '>', '▶': '>', '◀': '<', '▼': 'v', '▲': '^',
	// arrows
	'→': '>', '←': '<', '↑': '^', '↓': 'v', '⇣': 'v', '↶': '<', '↷': '>', '↻': '@',
	'⏎': '<', '⎇': '@',
	// bars and meters
	'█': '#', '▍': '|', '░': '.', '▰': '#', '▱': '-',
	// punctuation
	'·': '-', '—': '-', '…': '.',
}

// toPlain rewrites a rendered frame for plain mode. Escape sequences are
// ASCII and pass through; so do letters of any script, which are the
// user's text rather than the interface's.
func toPlain(s string) string {
	return strings.Map(func(r rune) rune {
		if a, ok := plainGlyphs[r]; ok {
			return a
		}
		if r >= 0x2800 && r <= 0x28FF { // braille: spinner frames
			return '*'
		}
		return r
	}, s)
}

// plainSpinner is a spinner that does not move: one frame, a tick an
// hour, so nothing on screen changes while a turn runs except its text.
var plainSpinner = spinner.Spinner{Frames: []string{"*"}, FPS: time.Hour}
