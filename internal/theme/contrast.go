package theme

import (
	"fmt"
	"math"
	"strings"
)

// Contrast is the WCAG relative-luminance contrast ratio between two
// colours, the same 1.0–21.0 measure the accessibility guidelines use.
// A theme's body text is expected to clear about 3.5:1 against the
// terminal background it assumes, which is the floor the palette comment
// in styles.go names; borders may sit lower.
//
// It is used both by the contrast test that gates the builtins and, in
// principle, by anyone auditing a user theme. ANSI palette indices carry
// no fixed sRGB value — they are whatever the user's terminal renders —
// so a role given as an index returns ok=false and is left to the
// terminal, exactly as the ANSI-16 fallback is.
func Contrast(a, b string) (ratio float64, ok bool) {
	la, oka := luminance(a)
	lb, okb := luminance(b)
	if !oka || !okb {
		return 0, false
	}
	hi, lo := la, lb
	if lo > hi {
		hi, lo = lo, hi
	}
	return (hi + 0.05) / (lo + 0.05), true
}

// AssumedBg is the terminal background a theme's contrast is judged
// against: near-black for a dark theme (the #1e1e1e the styles.go comment
// names), white for a light one.
func AssumedBg(dark bool) string {
	if dark {
		return "#1e1e1e"
	}
	return "#ffffff"
}

// luminance returns the WCAG relative luminance of a hex colour. An ANSI
// index has no fixed value, so ok is false for it.
func luminance(s string) (float64, bool) {
	r, g, b, ok := rgb(s)
	if !ok {
		return 0, false
	}
	lin := func(c float64) float64 {
		c /= 255
		if c <= 0.03928 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(r) + 0.7152*lin(g) + 0.0722*lin(b), true
}

// rgb parses a hex colour into 0–255 components.
func rgb(s string) (r, g, b float64, ok bool) {
	if !strings.HasPrefix(s, "#") {
		return 0, 0, 0, false
	}
	h := s[1:]
	if len(h) == 3 {
		h = string([]byte{h[0], h[0], h[1], h[1], h[2], h[2]})
	}
	if len(h) != 6 {
		return 0, 0, 0, false
	}
	var ri, gi, bi int
	if _, err := fmt.Sscanf(h, "%02x%02x%02x", &ri, &gi, &bi); err != nil {
		return 0, 0, 0, false
	}
	return float64(ri), float64(gi), float64(bi), true
}
