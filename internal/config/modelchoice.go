package config

import (
	"fmt"
	"strings"
)

// ModelChoice resolves UI selections. The two-word form pins a provider
// so identical model IDs sold by different gateways stay distinct.
func (c *Config) ModelChoice(arg string) (provider, model string, explicit bool, err error) {
	fields := strings.Fields(arg)
	if len(fields) == 0 || len(fields) > 2 {
		return "", "", false, fmt.Errorf("use /model [provider] <model>")
	}
	if len(fields) == 2 {
		provider, model, explicit = normalizeProviderName(fields[0]), fields[1], true
	} else if p, m, ref := OpenCodeModelRef(fields[0]); ref {
		provider, model, explicit = p, m, true
	} else {
		model = fields[0]
		if c != nil {
			provider, _ = c.RouteFor(model)
		}
		return
	}
	if c == nil {
		return "", "", false, fmt.Errorf("provider switching needs a configured provider")
	}
	if _, ok := c.Providers[provider]; !ok {
		return "", "", false, fmt.Errorf("provider %q is not configured; add it with /connect", provider)
	}
	return
}
