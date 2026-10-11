package tui

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/xmonader/janetcode/internal/prompt"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// pasteImage saves the clipboard image to a file and inserts @path.
func (m *model) pasteImage() {
	data, err := m.clipboard()
	if err != nil {
		m.status = err.Error()
		m.refresh(false)
		return
	}
	path, err := saveClipboardImage(data)
	if err != nil {
		m.status = "could not save the image: " + err.Error()
		m.refresh(false)
		return
	}
	v := m.input.Value()
	if v != "" && !strings.HasSuffix(v, " ") && !strings.HasSuffix(v, "\n") {
		m.input.InsertString(" ")
	}
	tok, ok := prompt.Mention(path)
	if !ok {
		tok = "@" + path
	}
	m.input.InsertString(tok + " ")
	m.layout()
	m.status = fmt.Sprintf("attached clipboard image (%s) · send when ready", textfmt.Bytes(int64(len(data))))
	m.refresh(false)
}

// A paste of any size arrives as one bracketed-paste key event. Over ten
// lines or a thousand characters it is kept aside and shown as a chip,
// so the prompt stays readable and a paste that ends in a newline cannot
// send itself.
const (
	pasteChipLines = 10
	pasteChipChars = 1000
)

var pasteMarkerRe = regexp.MustCompile(`\[paste #(\d+) \+\d+ lines?\]`)

// cleanPaste is a bracketed paste as text. Many terminals (VTE, xterm)
// send a pasted newline as a carriage return, so a long paste used to be
// kept behind its chip with no "\n" in it at all: the chip said "+1
// line", the model was sent one line joined by CRs, and the transcript
// echoed the CRs to the terminal, each one sending the cursor back to
// column 0 over the text already drawn. Terminal output copied with its
// colours carries escape sequences, which the chip likewise kept and
// wrote back raw. The textarea sanitises a short paste on its own; this
// gives the chip the same text.
func cleanPaste(s string) string {
	s = ansi.Strip(s)
	s = strings.ReplaceAll(s, "\r\n", "\n")
	if crPairsOnly(s) {
		// tmux's paste-buffer (without -r) and some terminals send every
		// line feed as a carriage return, so CRLF text arrives as \r\r
		// and came out double-spaced.
		s = strings.ReplaceAll(s, "\r\r", "\r")
	}
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f:
			return -1
		}
		return r
	}, s)
}

// crPairsOnly reports whether a paste breaks its lines with carriage
// returns alone, every run of them even: CRLF text whose line feeds were
// sent as carriage returns. Text whose every line break is a blank line
// reads the same; code never is, and prose loses only a blank line
// between paragraphs, where double spacing would put one in every line.
func crPairsOnly(s string) bool {
	if strings.Contains(s, "\n") || !strings.Contains(s, "\r\r") {
		return false
	}
	run := 0
	for i := 0; i <= len(s); i++ {
		if i < len(s) && s[i] == '\r' {
			run++
			continue
		}
		if run%2 != 0 {
			return false
		}
		run = 0
	}
	return true
}

// bigPaste reports whether a paste is large enough to be worth a chip.
func bigPaste(s string) bool {
	return strings.Count(s, "\n") >= pasteChipLines || len(s) > pasteChipChars
}

// storePaste keeps the text and returns the chip that stands for it.
func (m *model) storePaste(text string) string {
	m.pastes = append(m.pastes, text)
	return fmt.Sprintf("[paste #%d +%s]", len(m.pastes), textfmt.Plural(strings.Count(text, "\n")+1, "line"))
}

// inflatePastes puts the pasted text back in place of every chip.
func (m *model) inflatePastes(text string) string {
	if len(m.pastes) == 0 {
		return text
	}
	return pasteMarkerRe.ReplaceAllStringFunc(text, func(s string) string {
		n, err := strconv.Atoi(pasteMarkerRe.FindStringSubmatch(s)[1])
		if err != nil || n < 1 || n > len(m.pastes) {
			return s
		}
		return m.pastes[n-1]
	})
}
