package tool

import (
	"encoding/json"
	"strings"

	"github.com/xmonader/janetcode/internal/egress"
)

// The tools that send data off the machine say where, before they run,
// so the egress policy can ask about or refuse the call (see
// agent.Egressor).

// EgressHosts is the host the URL names.
func (FetchURL) EgressHosts(input json.RawMessage) []string {
	var in struct {
		URL string `json:"url"`
	}
	if json.Unmarshal(input, &in) != nil {
		return nil
	}
	if h := egress.HostOf(in.URL); h != "" {
		return []string{h}
	}
	return nil
}

// EgressHosts is the search provider's host: the query goes there.
func (t WebSearch) EgressHosts(json.RawMessage) []string {
	base := t.Config.URL
	if base == "" {
		base = duckduckgoURL
		if strings.ToLower(t.Config.Provider) == "brave" {
			base = braveURL
		}
	}
	if h := egress.HostOf(base); h != "" {
		return []string{h}
	}
	return nil
}

// EgressHosts are the hosts the command visibly reaches.
func (Bash) EgressHosts(input json.RawMessage) []string {
	var in struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(input, &in) != nil {
		return nil
	}
	return egress.HostsInCommand(in.Command)
}
