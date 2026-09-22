package config

import "strings"

// The model slots. A session runs on one model and may name others for
// particular jobs: a cheaper one for mechanical steps, a stronger one a
// failing turn escalates to, one the advisor consults, one that reads
// images. Every slot is optional and every empty slot means the session's
// own model, so a configuration that names none behaves as it always did.
//
// The point of naming them in one place is that the fallback is written
// once. Before this there were three ad-hoc rules — sideQuery's "empty
// model means the session's", the cascade's "no strong model, no
// cascade", the router's "a name nothing routes turns it off" — and each
// consumer had to remember its own. Slot answers the one question every
// front-end asks: which provider and model actually fill this slot right
// now.

// SlotNames are the slots beside the session's own model, in the order a
// picker lists them.
var SlotNames = []string{"cheap", "strong", "advisor", "vision"}

// slotField maps a slot name to where its model id lives in the config.
func slotField(c *Config, name string) *string {
	switch name {
	case "cheap":
		return &c.RouteCheap
	case "strong":
		return &c.StrongModel
	case "advisor":
		return &c.AdvisorModel
	case "vision":
		return &c.VisionModel
	}
	return nil
}

// SlotKey is the config.json key a slot is stored under, or "" when the
// name is not a slot. /config and /model share it so a slot cannot be
// settable under one spelling and not the other.
func SlotKey(name string) string {
	switch strings.TrimSpace(name) {
	case "cheap":
		return "route_cheap"
	case "strong":
		return "strong_model"
	case "advisor":
		return "advisor_model"
	case "vision":
		return "vision_model"
	case "main", "model":
		return "model"
	}
	return ""
}

// KnownSlot reports whether name is one of SlotNames.
func KnownSlot(name string) bool {
	c := Config{}
	return slotField(&c, strings.TrimSpace(name)) != nil
}

// SlotModel is the model id configured for a slot, empty when the slot is
// unset. It is the raw field: a caller asking "has the user chosen one?"
// wants this, and a caller asking "what will actually run?" wants Slot.
func (c *Config) SlotModel(name string) string {
	f := slotField(c, strings.TrimSpace(name))
	if f == nil {
		return ""
	}
	return strings.TrimSpace(*f)
}

// Slot resolves a slot to the provider and model that fill it. An unset
// slot — and a slot set to the session's own model — resolves to the
// session's provider and model, so nothing a consumer does with the
// answer can break on a slot nobody filled in.
//
// The provider is the one RouteFor names for that model, which may not be
// the active one: a slot may live on another configured provider exactly
// as a fallback model may. When nothing routes the name, the active
// provider is returned, which is what every caller would have fallen back
// to anyway.
//
// Slot answers what would run, not whether a feature is on. The cascade
// still needs escalate_after, step routing still needs route_cheap to
// have been named, and the advisor tool still appears only when
// advisor_model is set; those gates ask SlotModel, not Slot.
func (c *Config) Slot(name string) (provider, model string) {
	model = c.SlotModel(name)
	if model == "" {
		model = strings.TrimSpace(c.Model)
	}
	if prov, ok := c.RouteFor(model); ok {
		return prov, model
	}
	return c.Provider, model
}

// SlotIsMain reports whether a slot resolves to the same model the
// session runs on — unset, or set to the very same name. A consumer that
// would otherwise switch models pointlessly (paying a cold prompt cache
// for no change of model) asks this first.
func (c *Config) SlotIsMain(name string) bool {
	m := c.SlotModel(name)
	return m == "" || strings.EqualFold(m, strings.TrimSpace(c.Model))
}
