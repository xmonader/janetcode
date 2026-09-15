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
// and diffs agree: headings in colAccent, inline code in colTool, rules
// and code-block fences in colFaint. Italics are disabled for the reason
// the thinking style gives — terminals with no italic face draw SGR italic
// as reverse video, which is another unwanted block of colour.
func markdownStyle(dark bool) ansi.StyleConfig {
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

	// Headings: one accent colour for every level, no h1 bar.
	c.Heading.Color = pick(colAccent)
	c.Heading.Bold = &yes
	c.Heading.BackgroundColor = nil
	c.H1.Prefix, c.H1.Suffix = "# ", ""
	c.H1.Color = pick(colAccent)
	c.H1.BackgroundColor = nil
	c.H1.Bold = &yes

	// Inline code: colour only, never a background.
	c.Code.Prefix, c.Code.Suffix = "", ""
	c.Code.Color = pick(colTool)
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
	c.CodeBlock.Color = pick(colDim)

	// Everything else: no italics, palette colours for rules and links.
	c.Emph.Italic = &off
	c.HorizontalRule.Color = pick(colFaint)
	c.HorizontalRule.Format = "\n────\n"
	c.Link.Color = pick(colAccent)
	c.LinkText.Color = pick(colDim)
	c.BlockQuote.Color = pick(colDim)
	c.BlockQuote.Italic = &off
	return c
}
