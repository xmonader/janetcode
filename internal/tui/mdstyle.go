package tui

import (
	"github.com/charmbracelet/glamour/ansi"
	glamourstyles "github.com/charmbracelet/glamour/styles"
	"github.com/charmbracelet/lipgloss"
)

// markdownStyle is the glamour style assistant markdown is rendered with.
//
// glamour's standard "dark" and "light" styles paint a background behind
// inline code (SGR 48;5;236 on dark, 48;5;254 on light), behind a code
// block's chroma canvas, and behind an h1. On a terminal whose own
// background is anything but that exact shade those blocks read as a grey
// smear across the transcript. This style keeps glamour's syntax
// highlighting and drops every background: the terminal's own background
// shows through everywhere, exactly as it does for the rest of the TUI.
//
// The colours come from the palette in styles.go so markdown, tool lines
// and diffs agree: headings in accent, inline code in tool, rules
// and code-block fences in faint. Italics are disabled for the reason
// the thinking style gives — terminals with no italic face draw SGR italic
// as reverse video, which is another unwanted block of colour.
func markdownStyle(p palette, dark bool) ansi.StyleConfig {
	c := glamourstyles.LightStyleConfig
	if dark {
		c = glamourstyles.DarkStyleConfig
	}
	pick := func(a lipgloss.AdaptiveColor) *string {
		s := a.Light
		if dark {
			s = a.Dark
		}
		return &s
	}
	yes, off := true, false

	// Headings: no "#" marks and no h1 bar. The level shows in colour and
	// weight instead, the way a page shows it: the top two in the accent,
	// the third in the text colour, anything deeper dimmed. All bold.
	c.Heading.Color = pick(p.accent)
	c.Heading.Bold = &yes
	c.Heading.BackgroundColor = nil
	for i, h := range []*ansi.StyleBlock{&c.H1, &c.H2, &c.H3, &c.H4, &c.H5, &c.H6} {
		h.Prefix, h.Suffix = "", ""
		h.BackgroundColor = nil
		h.Bold = &yes
		switch {
		case i < 2:
			h.Color = pick(p.accent)
		case i == 2:
			h.Color = nil
		default:
			h.Color = pick(p.dim)
		}
	}

	// Inline code: colour only, never a background.
	c.Code.Prefix, c.Code.Suffix = "", ""
	c.Code.Color = pick(p.tool)
	c.Code.BackgroundColor = nil

	// Code blocks: syntax highlighting on the terminal's own background.
	if c.CodeBlock.Chroma != nil {
		chroma := *c.CodeBlock.Chroma // copy: the standard styles are shared
		chroma.Background = ansi.StylePrimitive{}
		chroma.Error.BackgroundColor = nil
		chroma.GenericEmph.Italic = &off
		c.CodeBlock.Chroma = &chroma
	}
	c.CodeBlock.BackgroundColor = nil
	c.CodeBlock.Color = pick(p.dim)

	// Everything else: no italics, palette colours for rules and links.
	c.Emph.Italic = &off
	c.HorizontalRule.Color = pick(p.faint)
	c.HorizontalRule.Format = "\n────\n"
	c.Link.Color = pick(p.accent)
	c.LinkText.Color = pick(p.dim)
	c.BlockQuote.Color = pick(p.dim)
	c.BlockQuote.Italic = &off
	return c
}
