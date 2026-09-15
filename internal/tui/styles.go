package tui

import (
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/xmonader/janetcode/internal/theme"
)

// The palette is a set of named colour roles, filled from the active
// theme (internal/theme) and adapting to the terminal background the mode
// settled on. Every foreground clears a contrast ratio of about 3.5:1
// against white for the light variant and against #1e1e1e for the dark
// one — the floor at which faint text stays readable, which the theme
// package's contrast test holds every builtin to; the borders are allowed
// to sit lower, but not so low that the input box disappears.
type palette struct {
	accent, dim, faint, warn, err, ok, user, tool     lipgloss.AdaptiveColor
	headBg, headFg, border, borderA, diffAdd, diffDel lipgloss.AdaptiveColor
}

// defaultPalette is the "default" builtin, so a build that never applies a
// theme (a test, an early frame) still draws the historical palette.
var defaultPalette = palette{
	accent:  lipgloss.AdaptiveColor{Light: "#0b63c6", Dark: "#7cc4ff"},
	dim:     lipgloss.AdaptiveColor{Light: "#6b7280", Dark: "#8b93a1"},
	faint:   lipgloss.AdaptiveColor{Light: "#7c8494", Dark: "#6b7484"},
	warn:    lipgloss.AdaptiveColor{Light: "#b45309", Dark: "#f5b642"},
	err:     lipgloss.AdaptiveColor{Light: "#b91c1c", Dark: "#ff6b6b"},
	ok:      lipgloss.AdaptiveColor{Light: "#15803d", Dark: "#5fd38d"},
	user:    lipgloss.AdaptiveColor{Light: "#1d4ed8", Dark: "#9ad0ff"},
	tool:    lipgloss.AdaptiveColor{Light: "#6d28d9", Dark: "#c4a7ff"},
	headBg:  lipgloss.AdaptiveColor{Light: "#e5e7eb", Dark: "#2b2f36"},
	headFg:  lipgloss.AdaptiveColor{Light: "#111827", Dark: "#e6e9ef"},
	border:  lipgloss.AdaptiveColor{Light: "#96a1b3", Dark: "#4b5361"},
	borderA: lipgloss.AdaptiveColor{Light: "#0b63c6", Dark: "#7cc4ff"},
	diffAdd: lipgloss.AdaptiveColor{Light: "#166534", Dark: "#7ee2a8"},
	diffDel: lipgloss.AdaptiveColor{Light: "#991b1b", Dark: "#ff8a8a"},
}

// A terminal that reports only the sixteen ANSI colours cannot be given
// truecolor hex: termenv quantises it to the nearest of the sixteen, and
// several of the palette's dark foregrounds land on black, which is the
// terminal's own background on a dark theme — invisible text. palette16
// names the ANSI slots outright, so every colour is one the terminal
// actually has and the user's own theme decides its shade.
var palette16 = palette{
	accent:  lipgloss.AdaptiveColor{Light: "4", Dark: "12"},
	dim:     lipgloss.AdaptiveColor{Light: "8", Dark: "7"},
	faint:   lipgloss.AdaptiveColor{Light: "8", Dark: "8"},
	warn:    lipgloss.AdaptiveColor{Light: "3", Dark: "11"},
	err:     lipgloss.AdaptiveColor{Light: "1", Dark: "9"},
	ok:      lipgloss.AdaptiveColor{Light: "2", Dark: "10"},
	user:    lipgloss.AdaptiveColor{Light: "4", Dark: "12"},
	tool:    lipgloss.AdaptiveColor{Light: "5", Dark: "13"},
	headBg:  lipgloss.AdaptiveColor{Light: "7", Dark: "8"},
	headFg:  lipgloss.AdaptiveColor{Light: "0", Dark: "15"},
	border:  lipgloss.AdaptiveColor{Light: "8", Dark: "8"},
	borderA: lipgloss.AdaptiveColor{Light: "4", Dark: "12"},
	diffAdd: lipgloss.AdaptiveColor{Light: "2", Dark: "10"},
	diffDel: lipgloss.AdaptiveColor{Light: "1", Dark: "9"},
}

// themePalette reads every role of a theme into a palette.
func themePalette(t theme.Theme) palette {
	get := func(role string) lipgloss.AdaptiveColor {
		c := t.Get(role)
		return lipgloss.AdaptiveColor{Light: c.Light, Dark: c.Dark}
	}
	return palette{
		accent: get(theme.RoleAccent), dim: get(theme.RoleDim), faint: get(theme.RoleFaint),
		warn: get(theme.RoleWarn), err: get(theme.RoleErr), ok: get(theme.RoleOK),
		user: get(theme.RoleUser), tool: get(theme.RoleTool),
		headBg: get(theme.RoleHeadBg), headFg: get(theme.RoleHeadFg),
		border: get(theme.RoleBorder), borderA: get(theme.RoleBorderActive),
		diffAdd: get(theme.RoleDiffAdd), diffDel: get(theme.RoleDiffDel),
	}
}

// styles is everything the interface draws with: the palette, every style
// derived from it, the theme's gutter and name, and whether the terminal
// is ANSI-only. A value is never changed once built; a theme switch builds
// a new one and the model swaps it in, so any number of models (and tests)
// can render at once.
type styles struct {
	col palette

	// gutter is the transcript's left gutter in columns, taken from the
	// theme. It is the base width; the frame collapses it to 0 on a narrow
	// terminal (see model.gutter).
	gutter int
	// themeName is the name of the theme applied, for the header, --doctor
	// and the /theme picker's highlight.
	themeName string
	// ansiOnly is true when the terminal reports only the sixteen ANSI
	// colours, so the default theme keeps the ANSI-safe palette.
	ansiOnly bool

	accent, dim, faint, warn, err, ok lipgloss.Style
	user, userBody, tool              lipgloss.Style
	diffAdd, diffDel                  lipgloss.Style
	header, headerHi, key             lipgloss.Style

	// Every box the footer can show is the same rounded border; only its
	// colour changes, and it says whose turn it is:
	//
	//   accent (borderA)  the box is waiting for you to type: idle,
	//                     plan mode, read-only, and the /connect dialog.
	//   dim (border)      the model has the turn; typing queues a
	//                     message instead of sending one.
	//   warn (warn)       the box is answering a warning: the permission
	//                     prompt, and the reason for a denial.
	//
	// model.placeholder names the same states in words, so the border and
	// the hint inside it never disagree.
	boxIdle, boxBusy, boxWarn, permBox lipgloss.Style

	dialog, section, selected, tag lipgloss.Style
}

// defaultStyles is what a build that never applies a theme draws with.
func defaultStyles() *styles {
	s := buildStyles(defaultPalette)
	s.gutter = theme.DefaultGutter
	s.themeName = theme.DefaultName
	return s
}

// newStyles builds the styles for a theme. The mode (dark) is already
// pinned on lipgloss by the caller, so the AdaptiveColor values render the
// right side; a single-mode theme fills both sides equally and renders the
// same either way.
func newStyles(t theme.Theme, ansiOnly bool) *styles {
	// A terminal that reports only sixteen colours cannot render a
	// truecolor palette without quantising it, and several dark
	// foregrounds then land on black. For the default theme we keep the
	// ANSI-16 palette there, so an ANSI-only terminal is exactly as it was
	// before themes existed. A theme chosen by name is honoured as given —
	// the mono theme is already built on ANSI indices for this case.
	p := themePalette(t)
	if ansiOnly && t.Name == theme.DefaultName {
		p = palette16
	}
	s := buildStyles(p)
	s.gutter = t.GutterWidth()
	s.themeName = t.Name
	s.ansiOnly = ansiOnly
	return s
}

// buildStyles derives every style from a palette.
func buildStyles(p palette) *styles {
	s := &styles{col: p}
	s.accent = lipgloss.NewStyle().Foreground(p.accent)
	s.dim = lipgloss.NewStyle().Foreground(p.dim)
	s.faint = lipgloss.NewStyle().Foreground(p.faint)
	s.warn = lipgloss.NewStyle().Foreground(p.warn)
	s.err = lipgloss.NewStyle().Foreground(p.err)
	s.ok = lipgloss.NewStyle().Foreground(p.ok)
	s.user = lipgloss.NewStyle().Foreground(p.user).Bold(true)
	s.userBody = lipgloss.NewStyle().Foreground(p.user)
	s.tool = lipgloss.NewStyle().Foreground(p.tool)
	s.diffAdd = lipgloss.NewStyle().Foreground(p.diffAdd)
	s.diffDel = lipgloss.NewStyle().Foreground(p.diffDel)
	s.header = lipgloss.NewStyle().Background(p.headBg).Foreground(p.headFg)
	s.headerHi = lipgloss.NewStyle().Background(p.headBg).Foreground(p.accent).Bold(true)
	s.key = lipgloss.NewStyle().Foreground(p.accent).Bold(true)
	s.boxIdle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder(), true).BorderForeground(p.borderA)
	s.boxBusy = lipgloss.NewStyle().Border(lipgloss.RoundedBorder(), true).BorderForeground(p.border)
	s.boxWarn = lipgloss.NewStyle().Border(lipgloss.RoundedBorder(), true).BorderForeground(p.warn)
	s.permBox = s.boxWarn.Padding(0, 1)
	s.dialog = s.boxIdle.Padding(0, 1) // the dialog is waiting for you: same border as an idle input box
	s.section = lipgloss.NewStyle().Foreground(p.faint).Bold(true)
	s.selected = lipgloss.NewStyle().Foreground(p.accent).Bold(true)
	s.tag = lipgloss.NewStyle().Foreground(p.ok)
	return s
}

// applyColorProfile settles what colour the interface may use at all and
// reports whether the terminal is ANSI-only. NO_COLOR is honoured whatever
// the terminal claims: every style renders plain. A terminal that reports
// only sixteen colours gets the ANSI palette instead of quantised truecolor.
func applyColorProfile(env func(string) string) (termenv.Profile, bool) {
	if env("NO_COLOR") != "" {
		lipgloss.SetColorProfile(termenv.Ascii)
		return termenv.Ascii, false
	}
	p := lipgloss.ColorProfile()
	return p, p == termenv.ANSI
}
