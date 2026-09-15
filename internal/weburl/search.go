package weburl

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// SearchConfig selects the search backend, the "search" object in the
// config file.
type SearchConfig struct {
	// Provider is "duckduckgo" (default, no key: the HTML endpoint is
	// scraped) or "brave" (the Search API; needs a key).
	Provider string `json:"provider,omitempty"`
	// Key is the API key; "${BRAVE_API_KEY}" style references are expanded
	// from the environment.
	Key string `json:"key,omitempty"`
	// URL overrides the endpoint (a self-hosted proxy, or tests).
	URL string `json:"url,omitempty"`
}

// Validate rejects unknown providers and a brave provider without a key.
func (c SearchConfig) Validate() error {
	switch strings.ToLower(c.Provider) {
	case "", "duckduckgo", "ddg":
		return nil
	case "brave":
		if os.ExpandEnv(c.Key) == "" {
			return errors.New("search: brave needs a key (BRAVE_API_KEY)")
		}
		return nil
	}
	return fmt.Errorf("search: unknown provider %q (duckduckgo or brave)", c.Provider)
}
