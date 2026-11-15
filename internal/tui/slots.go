package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/settingsapply"
)

// The model slots in the interface. A session runs on one model and may
// name others for particular jobs (see config/slot.go); /model picks the
// session's, and /model <slot> picks one of the others through the very
// same dialog, because a second model list with its own search and its
// own idea of what a provider is would be a second thing to keep right.
//
// What a slot picker does differently is what it does at the end: the
// session's model switches the running agent and the provider with it,
// while a slot writes one config key. Everything before that -- the
// provider's list, the prices, the free-text id, the search -- is shared.

// slotOff are the words that empty a slot, so a slot can be given back to
// the session's model without opening anything.
var slotOff = map[string]bool{"off": true, "none": true, "-": true, "main": true}

// cmdModelSlot handles /model <slot> [name|off]. It reports whether the
// first argument named a slot at all; when it did not, /model carries on
// treating it as a model id, which is what it has always done.
func (m *model) cmdModelSlot(fields []string) (tea.Cmd, bool, bool) {
	if len(fields) < 2 || !config.KnownSlot(fields[1]) {
		return nil, false, false
	}
	slot := strings.ToLower(fields[1])
	if m.opts.Config == nil {
		m.transcript.errorf("model slots need a config file")
		return nil, false, true
	}
	if len(fields) == 2 {
		return m.openSlot(slot), true, true
	}
	name := strings.TrimSpace(strings.Join(fields[2:], " "))
	if slotOff[strings.ToLower(name)] {
		name = ""
	}
	m.applySlot(slot, name)
	return nil, false, true
}

// applySlot writes a slot and says what it now resolves to. An empty name
// gives the slot back to the session's model, which is what an unset slot
// has always meant.
func (m *model) applySlot(slot, name string) {
	key := config.SlotKey(slot)
	if key == "" {
		m.transcript.errorf("%q is not a model slot (%s)", slot, strings.Join(config.SlotNames, ", "))
		return
	}
	if _, err := settingsapply.Apply(m.opts.Config, m.opts.Agent, m.opts.ConfigPath, key, name); err != nil {
		m.transcript.errorf("%v", err)
		return
	}
	m.note(m.slotLine(slot))
}

// slotLine is one slot's status: what fills it, or the main model it
// falls back to when nobody has filled it.
func (m *model) slotLine(slot string) string {
	cfg := m.opts.Config
	if cfg == nil {
		return slot + ": no config"
	}
	prov, model := cfg.Slot(slot)
	if cfg.SlotModel(slot) == "" {
		return slot + ": unset (main: " + prov + "/" + model + ")"
	}
	line := slot + ": " + model + " on " + prov
	if cfg.SlotIsMain(slot) {
		line += " (the session's own model)"
	}
	return line + slotEffect(slot, cfg)
}

// slotEffect says what a filled slot does not do on its own: three of the
// four need a second key before anything changes, and a slot that looks
// set while the feature is off is the confusion this whole command exists
// to remove.
func slotEffect(slot string, cfg *config.Config) string {
	switch slot {
	case "strong":
		if cfg.EscalateAfter <= 0 {
			return " · the cascade is off (/features escalate on)"
		}
	case "vision":
		return " · nothing reads it yet"
	case "advisor":
		if cfg.AdvisorMode != "always" {
			return " · offered as a tool, not forced (/features advisor on)"
		}
	}
	return ""
}

// slotLines is every slot's status, for /features and /model.
func (m *model) slotLines() []string {
	out := make([]string, 0, len(config.SlotNames)+1)
	if cfg := m.opts.Config; cfg != nil {
		out = append(out, "main: "+cfg.Model+" on "+cfg.Provider)
	}
	for _, s := range config.SlotNames {
		out = append(out, m.slotLine(s))
	}
	return out
}

// openSlot opens the model dialog for a slot: the provider's own list,
// searched and priced exactly as /model shows it, ending in one config
// key instead of a session-wide switch.
func (m *model) openSlot(slot string) tea.Cmd {
	cmd := m.openConnectFor(stepModel, slot)
	if m.connect == nil {
		return cmd // openConnect refused (mid-turn, or no provider builder)
	}
	m.connect.slot = slot
	m.transcript.info(m.slotLine(slot))
	return cmd
}

// applySlotChoice ends a slot dialog: the model list has already been
// fetched from the provider, so the details it reported are worth
// keeping even though no session model changed.
func (m *model) applySlotChoice(modelID string) {
	c := m.connect
	if mi, ok := c.meta[modelID]; ok {
		m.rememberModel(mi)
	}
	slot := c.slot
	m.closeConnect("")
	m.applySlot(slot, modelID)
}
