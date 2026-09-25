package tui

import (
	"fmt"
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
	accent:  lipgloss.AdaptiveColor{Light: "#5b3df5", Dark: "#a594ff"},
	dim:     lipgloss.AdaptiveColor{Light: "#636366", Dark: "#98989f"},
	faint:   lipgloss.AdaptiveColor{Light: "#6e6e73", Dark: "#7c7c82"},
	warn:    lipgloss.AdaptiveColor{Light: "#a94f00", Dark: "#ffb340"},
	err:     lipgloss.AdaptiveColor{Light: "#c8001a", Dark: "#ff6961"},
	ok:      lipgloss.AdaptiveColor{Light: "#1a7f37", Dark: "#4cd964"},
	user:    lipgloss.AdaptiveColor{Light: "#4a2bd9", Dark: "#c9c0ff"},
	tool:    lipgloss.AdaptiveColor{Light: "#007f8a", Dark: "#5fe3d8"},
	headBg:  lipgloss.AdaptiveColor{Light: "#e8e8ed", Dark: "#2c2c2e"},
	headFg:  lipgloss.AdaptiveColor{Light: "#1d1d1f", Dark: "#f2f2f7"},
	border:  lipgloss.AdaptiveColor{Light: "#8e8e93", Dark: "#48484a"},
	borderA: lipgloss.AdaptiveColor{Light: "#5b3df5", Dark: "#a594ff"},
	diffAdd: lipgloss.AdaptiveColor{Light: "#1a7f37", Dark: "#6ee08a"},
	diffDel: lipgloss.AdaptiveColor{Light: "#c8001a", Dark: "#ff8a80"},
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
	// The diff's line tints: a changed line's background across the width,
	// and a stronger one behind the words that changed. diffTint is false
	// for a palette they cannot be mixed from (ANSI indices).
	diffAddBg, diffDelBg, diffAddHi, diffDelHi lipgloss.Style
	diffTint                                   bool
	header, headerHi, key, heading             lipgloss.Style

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
	addBg, ok1 := tint(p.diffAdd, 0.2)
	delBg, ok2 := tint(p.diffDel, 0.2)
	addHi, ok3 := tint(p.diffAdd, 0.42)
	delHi, ok4 := tint(p.diffDel, 0.42)
	if s.diffTint = ok1 && ok2 && ok3 && ok4; s.diffTint {
		s.diffAddBg = s.diffAdd.Background(addBg)
		s.diffDelBg = s.diffDel.Background(delBg)
		s.diffAddHi = s.diffAdd.Background(addHi).Bold(true)
		s.diffDelHi = s.diffDel.Background(delHi).Bold(true)
	}
	s.header = lipgloss.NewStyle().Background(p.headBg).Foreground(p.headFg)
	s.headerHi = lipgloss.NewStyle().Background(p.headBg).Foreground(p.accent).Bold(true)
	s.key = lipgloss.NewStyle().Foreground(p.accent).Bold(true)
	s.heading = lipgloss.NewStyle().Foreground(p.headFg).Bold(true)
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

// tint is c laid thinly over the terminal's background: mixed into black
// on the dark side and into white on the light one, frac of the way. It
// reports false for a colour that is not a hex value on both sides.
func tint(c lipgloss.AdaptiveColor, frac float64) (lipgloss.AdaptiveColor, bool) {
	dark, ok1 := mixHex(c.Dark, [3]float64{0, 0, 0}, frac)
	light, ok2 := mixHex(c.Light, [3]float64{255, 255, 255}, frac)
	return lipgloss.AdaptiveColor{Light: light, Dark: dark}, ok1 && ok2
}

func mixHex(hex string, base [3]float64, frac float64) (string, bool) {
	if len(hex) != 7 || hex[0] != '#' {
		return "", false
	}
	var rgb [3]int
	if _, err := fmt.Sscanf(hex[1:], "%02x%02x%02x", &rgb[0], &rgb[1], &rgb[2]); err != nil {
		return "", false
	}
	var out [3]int
	for i := range rgb {
		out[i] = int(base[i] + (float64(rgb[i])-base[i])*frac + 0.5)
	}
	return fmt.Sprintf("#%02x%02x%02x", out[0], out[1], out[2]), true
}
