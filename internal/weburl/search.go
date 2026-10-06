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
	// scraped), "brave", "tavily", "serper" or "exa" (APIs that need a
	// key), or "searxng" (a self-hosted instance at URL).
	Provider string `json:"provider,omitempty"`
	// Key is the API key; "${BRAVE_API_KEY}" style references are expanded
	// from the environment. Empty reads the provider's own variable
	// (BRAVE_API_KEY, TAVILY_API_KEY, SERPER_API_KEY, EXA_API_KEY).
	Key string `json:"key,omitempty"`
	// URL overrides the endpoint (a self-hosted proxy, or tests).
	URL string `json:"url,omitempty"`
}

// Validate rejects unknown providers, a keyed provider with no key in the
// config or its environment variable, and SearXNG without a URL.
func (c SearchConfig) Validate() error {
	p := strings.ToLower(c.Provider)
	switch p {
	case "", "duckduckgo", "ddg":
		return nil
	case "searxng":
		if c.URL == "" {
			return errors.New("search: searxng needs the instance's url")
		}
		return nil
	}
	env, ok := SearchKeyEnv[p]
	if !ok {
		return fmt.Errorf("search: unknown provider %q (duckduckgo, brave, tavily, serper, exa or searxng)", c.Provider)
	}
	if os.ExpandEnv(c.Key) == "" && os.Getenv(env) == "" {
		return fmt.Errorf("search: %s needs a key (%s)", p, env)
	}
	return nil
}

// SearchKeyEnv is where each keyed provider's key is read from when the
// config gives none.
var SearchKeyEnv = map[string]string{"brave": "BRAVE_API_KEY", "tavily": "TAVILY_API_KEY", "serper": "SERPER_API_KEY", "exa": "EXA_API_KEY"}
