package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/theme"
)

// themeCommand implements /theme: alone it opens the searchable picker,
// "list" prints the themes, and a name switches to it live and saves the
// choice. The switch rebuilds the palette and every style through the same
// path startup uses (applyTheme → buildStyles) and re-renders the whole
// transcript in place; the terminal background is never re-probed, so no
// keystroke is eaten.
func (m *model) themeCommand(args []string) tea.Cmd {
	if len(args) == 0 {
		return m.openThemePicker()
	}
	switch strings.ToLower(args[0]) {
	case "list", "ls":
		m.transcript.info(m.themeList())
		return nil
	}
	m.applyThemeChoice(strings.TrimSpace(args[0]), true)
	return nil
}

// themeList is the plain-text listing for /theme list, with the active one
// marked and each theme's mode and description.
func (m *model) themeList() string {
	set := themeSetOr(m.opts.Themes)
	var sb strings.Builder
	sb.WriteString("auto" + strings.Repeat(" ", 16-4) + "follow the terminal background\n")
	for _, t := range set.Sorted() {
		mark := "  "
		if t.Name == m.opts.Theme {
			mark = "▸ "
		}
		mode := t.Mode()
		fmt.Fprintf(&sb, "%s%-14s %-7s %s\n", mark, t.Name, mode, t.Desc)
	}
	sb.WriteString("\nselect one with /theme <name>, or /theme alone for a searchable list")
	return strings.TrimRight(sb.String(), "\n")
}

// applyThemeChoice switches to a theme by name (or auto/dark/light),
// rebuilds the palette and styles, recolours the transcript, and — when
// persist is set — writes just the theme key of the config file.
func (m *model) applyThemeChoice(name string, persist bool) {
	set := themeSetOr(m.opts.Themes)
	lower := strings.ToLower(strings.TrimSpace(name))
	switch lower {
	case "auto", "dark", "light":
		name = lower
	default:
		if !set.Has(name) {
			m.transcript.errorf("unknown theme %q · /theme list shows them", name)
			m.refresh(false)
			return
		}
	}
	th, dark := resolveTheme(name, set, m.bgDark)
	lipgloss.SetHasDarkBackground(dark)
	applyTheme(th)
	mode := "light"
	if dark {
		mode = "dark"
	}
	m.transcript.restyle(mode)
	m.opts.Theme = name
	if cfg := m.opts.Config; cfg != nil {
		cfg.Theme = name
	}
	m.spinner.Style = styleAccent
	m.note(m.themeApplied(name, th, mode))
	if persist && m.opts.ConfigPath != "" {
		if err := config.UpdateKey(m.opts.ConfigPath, "theme", name); err != nil {
			m.transcript.errorf("saving theme: %v", err)
		}
	}
	m.layout()
	m.refresh(true)
}

// themeApplied is the one-line confirmation after a switch.
func (m *model) themeApplied(name string, th theme.Theme, mode string) string {
	switch name {
	case "auto":
		return fmt.Sprintf("theme: auto (%s) · follows the terminal background", mode)
	case "dark", "light":
		return fmt.Sprintf("theme: %s · the default palette, %s mode", name, name)
	}
	return fmt.Sprintf("theme: %s (%s) · %s", name, mode, th.Desc)
}

// openThemePicker shows the searchable list of themes, applying the one
// chosen on enter. A row shows the name, its mode and its description; the
// active theme is tagged.
func (m *model) openThemePicker() tea.Cmd {
	set := themeSetOr(m.opts.Themes)
	rows := []row{{
		key:   "auto detect terminal background",
		cells: []string{"auto", "auto", "follow the terminal background"},
		name:  "auto",
	}}
	for _, t := range set.Sorted() {
		mode := t.Mode()
		tag := ""
		if t.Name == m.opts.Theme {
			tag = "current"
		}
		rows = append(rows, row{
			key:   t.Name + " " + t.Desc + " " + mode,
			cells: []string{t.Name, mode, t.Desc},
			tag:   tag,
			name:  t.Name,
		})
	}
	return m.openPicker(&picker{
		title:    "Switch theme",
		hint:     "enter applies it live and saves the choice · type to search",
		rows:     rows,
		freeText: true,
		choose: func(r row, typed string) tea.Cmd {
			name := r.name
			if name == "" {
				name = typed
			}
			m.applyThemeChoice(name, true)
			return nil
		},
	})
}

// completeTheme offers theme names for the /theme argument menu.
func (m *model) completeTheme(partial string) []command {
	set := themeSetOr(m.opts.Themes)
	var out []command
	if partial == "" || strings.HasPrefix("auto", strings.ToLower(partial)) {
		out = append(out, command{name: "/theme auto", help: "follow the terminal background"})
	}
	for _, t := range set.Sorted() {
		if partial != "" && !strings.Contains(strings.ToLower(t.Name), strings.ToLower(partial)) {
			continue
		}
		out = append(out, command{name: "/theme " + t.Name, help: t.Desc})
	}
	return out
}

// themeSetOr returns the given set or, when it is nil (a test, or a
// session built without a config), a fresh set of just the builtins.
func themeSetOr(set *theme.Set) *theme.Set {
	if set != nil {
		return set
	}
	s, _ := theme.Build(nil)
	return s
}

// resolveTheme turns the configured theme name into the Theme to draw and
// the mode (dark) to draw it in. auto follows the terminal background;
// dark and light pin the default theme's mode; a named theme is drawn as
// itself — an adaptive one still follows the terminal, a single-mode one
// pins the background to its own light/dark, which is what "selecting a
// dark-only theme pins the assumption" means. detectedDark is the
// terminal's real background, probed once, so switching never re-probes.
func resolveTheme(name string, set *theme.Set, detectedDark bool) (theme.Theme, bool) {
	set = themeSetOr(set)
	def := set.Default()
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "auto":
		return def, detectedDark
	case "dark":
		return def, true
	case "light":
		return def, false
	}
	if t, ok := set.Get(name); ok {
		if t.Adaptive() {
			return t, detectedDark
		}
		return t, t.Dark
	}
	// The config validated the name, so this is only reached by a caller
	// with no set; fall back to the default in the detected mode.
	return def, detectedDark
}
