package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// The brand in the terminal: the spark the web mark dots its j with, and
// the name in the mark's gradient, iris into aqua. A terminal limited to
// sixteen colours gets the name in the accent instead: a gradient
// quantised to four colours is a stripe, not a sheen.

const brandSpark = "✦"

// The gradient's two ends, for a dark and a light terminal. The light
// pair is darker so the name stays readable on white.
var (
	brandFromDark, brandToDark   = [3]int{0xa5, 0x94, 0xff}, [3]int{0x5f, 0xe3, 0xd8}
	brandFromLight, brandToLight = [3]int{0x5b, 0x3d, 0xf5}, [3]int{0x00, 0x7f, 0x8a}
)

// brandName draws "✦ janetcode" in the brand gradient, bold.
func brandName(sty *styles, dark bool) string {
	word := brandSpark + " janetcode"
	if sty.ansiOnly {
		return sty.accent.Bold(true).Render(word)
	}
	from, to := brandFromLight, brandToLight
	if dark {
		from, to = brandFromDark, brandToDark
	}
	runes := []rune(word)
	var sb strings.Builder
	for i, r := range runes {
		t := float64(i) / float64(len(runes)-1)
		c := fmt.Sprintf("#%02x%02x%02x",
			from[0]+int(t*float64(to[0]-from[0])),
			from[1]+int(t*float64(to[1]-from[1])),
			from[2]+int(t*float64(to[2]-from[2])))
		sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(c)).Bold(true).Render(string(r)))
	}
	return sb.String()
}
