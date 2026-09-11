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
//
// The defaults here are the "default" builtin, so a build that never
// applies a theme (a test, an early frame) still draws the historical
// palette. applyTheme replaces them from any theme; buildStyles then
// rebuilds every style below.
var (
	colAccent  = lipgloss.AdaptiveColor{Light: "#0b63c6", Dark: "#7cc4ff"}
	colDim     = lipgloss.AdaptiveColor{Light: "#6b7280", Dark: "#8b93a1"}
	colFaint   = lipgloss.AdaptiveColor{Light: "#7c8494", Dark: "#6b7484"}
	colWarn    = lipgloss.AdaptiveColor{Light: "#b45309", Dark: "#f5b642"}
	colErr     = lipgloss.AdaptiveColor{Light: "#b91c1c", Dark: "#ff6b6b"}
	colOK      = lipgloss.AdaptiveColor{Light: "#15803d", Dark: "#5fd38d"}
	colUser    = lipgloss.AdaptiveColor{Light: "#1d4ed8", Dark: "#9ad0ff"}
	colTool    = lipgloss.AdaptiveColor{Light: "#6d28d9", Dark: "#c4a7ff"}
	colHeadBg  = lipgloss.AdaptiveColor{Light: "#e5e7eb", Dark: "#2b2f36"}
	colHeadFg  = lipgloss.AdaptiveColor{Light: "#111827", Dark: "#e6e9ef"}
	colBorder  = lipgloss.AdaptiveColor{Light: "#96a1b3", Dark: "#4b5361"}
	colBorderA = lipgloss.AdaptiveColor{Light: "#0b63c6", Dark: "#7cc4ff"}
	colDiffAdd = lipgloss.AdaptiveColor{Light: "#166534", Dark: "#7ee2a8"}
	colDiffDel = lipgloss.AdaptiveColor{Light: "#991b1b", Dark: "#ff8a8a"}
)

// activeGutter is the transcript's left gutter in columns, taken from the
// active theme. It is the base width; the frame collapses it to 0 on a
// narrow terminal (see model.gutter). The default matches the "default"
// theme so a build that never applies a theme still breathes.
var activeGutter = theme.DefaultGutter

// activeThemeName is the name of the theme currently applied, for the
// header, --doctor and the /theme picker's highlight.
var activeThemeName = theme.DefaultName

// applyTheme replaces the palette from a theme and rebuilds every style.
// The mode (dark) is already pinned on lipgloss by the caller, so the
// AdaptiveColor values render the right side; a single-mode theme fills
// both sides equally and renders the same either way.
func applyTheme(t theme.Theme) {
	// A terminal that reports only sixteen colours cannot render a
	// truecolor palette without quantising it, and several dark
	// foregrounds then land on black. For the default theme we keep the
	// ANSI-16 palette there, so an ANSI-only terminal is exactly as it was
	// before themes existed. A theme chosen by name is honoured as given —
	// the mono theme is already built on ANSI indices for this case.
	if ansiOnly && t.Name == theme.DefaultName {
		usePalette16()
		activeThemeName = t.Name
		activeGutter = t.GutterWidth()
		return
	}
	set := func(dst *lipgloss.AdaptiveColor, role string) {
		c := t.Get(role)
		*dst = lipgloss.AdaptiveColor{Light: c.Light, Dark: c.Dark}
	}
	set(&colAccent, theme.RoleAccent)
	set(&colDim, theme.RoleDim)
	set(&colFaint, theme.RoleFaint)
	set(&colWarn, theme.RoleWarn)
	set(&colErr, theme.RoleErr)
	set(&colOK, theme.RoleOK)
	set(&colUser, theme.RoleUser)
	set(&colTool, theme.RoleTool)
	set(&colHeadBg, theme.RoleHeadBg)
	set(&colHeadFg, theme.RoleHeadFg)
	set(&colBorder, theme.RoleBorder)
	set(&colBorderA, theme.RoleBorderActive)
	set(&colDiffAdd, theme.RoleDiffAdd)
	set(&colDiffDel, theme.RoleDiffDel)
	activeGutter = t.GutterWidth()
	activeThemeName = t.Name
	buildStyles()
}

// styles are rebuilt whenever the palette changes, so a fallback palette
// reaches every line the interface draws and not only the ones built
// after the switch.
var (
	styleAccent   lipgloss.Style
	styleDim      lipgloss.Style
	styleFaint    lipgloss.Style
	styleWarn     lipgloss.Style
	styleErr      lipgloss.Style
	styleOK       lipgloss.Style
	styleUser     lipgloss.Style
	styleUserBody lipgloss.Style
	styleTool     lipgloss.Style
	styleDiffAdd  lipgloss.Style
	styleDiffDel  lipgloss.Style
	styleHeader   lipgloss.Style
	styleHeaderHi lipgloss.Style
	styleKey      lipgloss.Style

	// Every box the footer can show is the same rounded border; only its
	// colour changes, and it says whose turn it is:
	//
	//   accent (colBorderA)  the box is waiting for you to type: idle,
	//                        plan mode, read-only, and the /connect dialog.
	//   dim (colBorder)      the model has the turn; typing queues a
	//                        message instead of sending one.
	//   warn (colWarn)       the box is answering a warning: the permission
	//                        prompt, and the reason for a denial.
	//
	// model.placeholder names the same states in words, so the border and
	// the hint inside it never disagree.
	styleBoxIdle lipgloss.Style
	styleBoxBusy lipgloss.Style
	styleBoxWarn lipgloss.Style
	stylePermBox lipgloss.Style

	styleDialog   lipgloss.Style
	styleSection  lipgloss.Style
	styleSelected lipgloss.Style
	styleTag      lipgloss.Style
)

func init() { buildStyles() }

// buildStyles derives every style from the current palette.
func buildStyles() {
	styleAccent = lipgloss.NewStyle().Foreground(colAccent)
	styleDim = lipgloss.NewStyle().Foreground(colDim)
	styleFaint = lipgloss.NewStyle().Foreground(colFaint)
	styleWarn = lipgloss.NewStyle().Foreground(colWarn)
	styleErr = lipgloss.NewStyle().Foreground(colErr)
	styleOK = lipgloss.NewStyle().Foreground(colOK)
	styleUser = lipgloss.NewStyle().Foreground(colUser).Bold(true)
	styleUserBody = lipgloss.NewStyle().Foreground(colUser)
	styleTool = lipgloss.NewStyle().Foreground(colTool)
	styleDiffAdd = lipgloss.NewStyle().Foreground(colDiffAdd)
	styleDiffDel = lipgloss.NewStyle().Foreground(colDiffDel)
	styleHeader = lipgloss.NewStyle().Background(colHeadBg).Foreground(colHeadFg)
	styleHeaderHi = lipgloss.NewStyle().Background(colHeadBg).Foreground(colAccent).Bold(true)
	styleKey = lipgloss.NewStyle().Foreground(colAccent).Bold(true)
	styleBoxIdle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder(), true).BorderForeground(colBorderA)
	styleBoxBusy = lipgloss.NewStyle().Border(lipgloss.RoundedBorder(), true).BorderForeground(colBorder)
	styleBoxWarn = lipgloss.NewStyle().Border(lipgloss.RoundedBorder(), true).BorderForeground(colWarn)
	stylePermBox = styleBoxWarn.Padding(0, 1)
	styleDialog = styleBoxIdle.Padding(0, 1) // the dialog is waiting for you: same border as an idle input box
	styleSection = lipgloss.NewStyle().Foreground(colFaint).Bold(true)
	styleSelected = lipgloss.NewStyle().Foreground(colAccent).Bold(true)
	styleTag = lipgloss.NewStyle().Foreground(colOK)
}

// A terminal that reports only the sixteen ANSI colours cannot be given
// truecolor hex: termenv quantises it to the nearest of the sixteen, and
// several of the palette's dark foregrounds land on black, which is the
// terminal's own background on a dark theme — invisible text. The
// fallback names the ANSI slots outright, so every colour is one the
// terminal actually has and the user's own theme decides its shade.
func usePalette16() {
	colAccent = lipgloss.AdaptiveColor{Light: "4", Dark: "12"}
	colDim = lipgloss.AdaptiveColor{Light: "8", Dark: "7"}
	colFaint = lipgloss.AdaptiveColor{Light: "8", Dark: "8"}
	colWarn = lipgloss.AdaptiveColor{Light: "3", Dark: "11"}
	colErr = lipgloss.AdaptiveColor{Light: "1", Dark: "9"}
	colOK = lipgloss.AdaptiveColor{Light: "2", Dark: "10"}
	colUser = lipgloss.AdaptiveColor{Light: "4", Dark: "12"}
	colTool = lipgloss.AdaptiveColor{Light: "5", Dark: "13"}
	colHeadBg = lipgloss.AdaptiveColor{Light: "7", Dark: "8"}
	colHeadFg = lipgloss.AdaptiveColor{Light: "0", Dark: "15"}
	colBorder = lipgloss.AdaptiveColor{Light: "8", Dark: "8"}
	colBorderA = lipgloss.AdaptiveColor{Light: "4", Dark: "12"}
	colDiffAdd = lipgloss.AdaptiveColor{Light: "2", Dark: "10"}
	colDiffDel = lipgloss.AdaptiveColor{Light: "1", Dark: "9"}
	buildStyles()
}

// applyColorProfile settles what colour the interface may use at all.
// NO_COLOR is honoured whatever the terminal claims: every style renders
// plain. A terminal that reports only sixteen colours gets the ANSI
// palette instead of quantised truecolor.
func applyColorProfile(env func(string) string) termenv.Profile {
	if env("NO_COLOR") != "" {
		lipgloss.SetColorProfile(termenv.Ascii)
		ansiOnly = false
		return termenv.Ascii
	}
	p := lipgloss.ColorProfile()
	ansiOnly = p == termenv.ANSI
	if ansiOnly {
		usePalette16()
	}
	return p
}

// ansiOnly is true when the terminal reports only the sixteen ANSI colours,
// so applyTheme keeps the ANSI-safe palette for the default theme.
var ansiOnly bool
