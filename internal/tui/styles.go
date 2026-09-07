package tui

import "github.com/charmbracelet/lipgloss"

// Colours adapt to the detected terminal background.
var (
	colAccent  = lipgloss.AdaptiveColor{Light: "#0b63c6", Dark: "#7cc4ff"}
	colDim     = lipgloss.AdaptiveColor{Light: "#6b7280", Dark: "#8b93a1"}
	colFaint   = lipgloss.AdaptiveColor{Light: "#9ca3af", Dark: "#5c6370"}
	colWarn    = lipgloss.AdaptiveColor{Light: "#b45309", Dark: "#f5b642"}
	colErr     = lipgloss.AdaptiveColor{Light: "#b91c1c", Dark: "#ff6b6b"}
	colOK      = lipgloss.AdaptiveColor{Light: "#15803d", Dark: "#5fd38d"}
	colUser    = lipgloss.AdaptiveColor{Light: "#1d4ed8", Dark: "#9ad0ff"}
	colTool    = lipgloss.AdaptiveColor{Light: "#6d28d9", Dark: "#c4a7ff"}
	colHeadBg  = lipgloss.AdaptiveColor{Light: "#e5e7eb", Dark: "#2b2f36"}
	colHeadFg  = lipgloss.AdaptiveColor{Light: "#111827", Dark: "#e6e9ef"}
	colBorder  = lipgloss.AdaptiveColor{Light: "#cbd5e1", Dark: "#3f4650"}
	colBorderA = lipgloss.AdaptiveColor{Light: "#0b63c6", Dark: "#7cc4ff"}
	colDiffAdd = lipgloss.AdaptiveColor{Light: "#166534", Dark: "#7ee2a8"}
	colDiffDel = lipgloss.AdaptiveColor{Light: "#991b1b", Dark: "#ff8a8a"}
)

var (
	styleAccent   = lipgloss.NewStyle().Foreground(colAccent)
	styleDim      = lipgloss.NewStyle().Foreground(colDim)
	styleFaint    = lipgloss.NewStyle().Foreground(colFaint)
	styleWarn     = lipgloss.NewStyle().Foreground(colWarn)
	styleErr      = lipgloss.NewStyle().Foreground(colErr)
	styleOK       = lipgloss.NewStyle().Foreground(colOK)
	styleUser     = lipgloss.NewStyle().Foreground(colUser).Bold(true)
	styleUserBody = lipgloss.NewStyle().Foreground(colUser)
	styleTool     = lipgloss.NewStyle().Foreground(colTool)
	styleDiffAdd  = lipgloss.NewStyle().Foreground(colDiffAdd)
	styleDiffDel  = lipgloss.NewStyle().Foreground(colDiffDel)
	styleHeader   = lipgloss.NewStyle().Background(colHeadBg).Foreground(colHeadFg)
	styleHeaderHi = lipgloss.NewStyle().Background(colHeadBg).Foreground(colAccent).Bold(true)
	styleKey      = lipgloss.NewStyle().Foreground(colAccent).Bold(true)

	styleInputBox = lipgloss.NewStyle().Border(lipgloss.RoundedBorder(), true).BorderForeground(colBorder)
	styleInputHot = lipgloss.NewStyle().Border(lipgloss.RoundedBorder(), true).BorderForeground(colBorderA)
	stylePermBox  = lipgloss.NewStyle().Border(lipgloss.RoundedBorder(), true).BorderForeground(colWarn).Padding(0, 1)
)
