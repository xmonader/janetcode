package mcpcmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/cmdresult"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// "/mcp search" looks a server up in the official MCP registry and prints
// the /mcp add line that would add it. It only reads: nothing is written
// or started until that line is run, so the user sees the exact command
// and arguments first, the same rule --from follows for a repository.

// Registry is the official MCP registry's server list. JANETCODE_MCP_REGISTRY
// points the search at another server with the same API.
const Registry = "https://registry.modelcontextprotocol.io/v0.1/servers"

// searchLimit is how many servers one search lists.
const searchLimit = 10

// Hit is one server a search found.
type Hit struct {
	Name  string // the registry's name, e.g. io.github.owner/server
	Title string
	Blurb string
	// Add is the /mcp add line, with <placeholders> for what the user
	// must fill in, or "" when the server cannot be added from a line.
	Add string
	// Notes say what Add needs that it cannot carry, or why there is no Add.
	Notes []string
}

type registryList struct {
	Servers []struct {
		Server registryServer `json:"server"`
	} `json:"servers"`
}

type registryServer struct {
	Name        string `json:"name"`
	Title       string `json:"title"`
	Description string `json:"description"`
	WebsiteURL  string `json:"websiteUrl"`
	Packages    []struct {
		RegistryType string `json:"registryType"`
		Identifier   string `json:"identifier"`
		RuntimeHint  string `json:"runtimeHint"`
		Transport    struct {
			Type string `json:"type"`
		} `json:"transport"`
		PackageArguments     []registryArg `json:"packageArguments"`
		EnvironmentVariables []registryKV  `json:"environmentVariables"`
	} `json:"packages"`
	Remotes []struct {
		Type    string       `json:"type"`
		URL     string       `json:"url"`
		Headers []registryKV `json:"headers"`
	} `json:"remotes"`
}

type registryArg struct {
	Type       string `json:"type"`
	Name       string `json:"name"`
	Value      string `json:"value"`
	IsRequired bool   `json:"isRequired"`
}

type registryKV struct {
	Name       string `json:"name"`
	Value      string `json:"value"`
	IsRequired bool   `json:"isRequired"`
	IsSecret   bool   `json:"isSecret"`
}

// Search asks the registry for servers matching q.
func Search(ctx context.Context, client *http.Client, q string) ([]Hit, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return nil, fmt.Errorf("usage: /mcp search <words>, e.g. /mcp search postgres")
	}
	base := Registry
	if v := strings.TrimSpace(os.Getenv("JANETCODE_MCP_REGISTRY")); v != "" {
		base = v
	}
	u, err := url.Parse(base)
	if err != nil {
		return nil, err
	}
	qs := u.Query()
	qs.Set("search", q)
	qs.Set("version", "latest")
	qs.Set("limit", fmt.Sprint(searchLimit))
	u.RawQuery = qs.Encode()
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("the MCP registry did not answer: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("reading the MCP registry's answer: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the MCP registry answered %d: %s", resp.StatusCode, textfmt.Clip(textfmt.OneLine(string(body)), 200))
	}
	var list registryList
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("the MCP registry's answer is not a server list: %w", err)
	}
	hits := make([]Hit, 0, len(list.Servers))
	for _, e := range list.Servers {
		if e.Server.Name != "" {
			hits = append(hits, hitOf(e.Server))
		}
	}
	return hits, nil
}

// hitOf turns a registry entry into the /mcp add line for it. A package
// that runs locally over stdio is preferred to a remote URL: it needs no
// account with a hosting service.
func hitOf(s registryServer) Hit {
	h := Hit{Name: s.Name, Title: s.Title, Blurb: strings.TrimSpace(s.Description)}
	slug := slugOf(s.Name)
	for _, p := range s.Packages {
		if p.Identifier == "" || (p.Transport.Type != "" && p.Transport.Type != "stdio") {
			continue
		}
		var cmd []string
		switch strings.ToLower(p.RegistryType) {
		case "npm":
			cmd = []string{"npx", "-y", p.Identifier}
		case "pypi":
			cmd = []string{"uvx", p.Identifier}
		case "oci":
			cmd = []string{"docker", "run", "-i", "--rm", p.Identifier}
		default:
			continue
		}
		if p.RuntimeHint != "" && p.RuntimeHint != cmd[0] {
			continue // a runner this line would not name correctly
		}
		for _, a := range p.PackageArguments {
			switch {
			case a.Type == "positional" && a.Value != "":
				cmd = append(cmd, a.Value)
			case a.Type == "positional" && a.IsRequired:
				cmd = append(cmd, "<"+orWord(a.Name, "argument")+">")
			case a.Type == "named" && a.Name != "" && (a.Value != "" || a.IsRequired):
				cmd = append(cmd, a.Name, orWord(a.Value, "<value>"))
			}
		}
		var opts []string
		for _, e := range p.EnvironmentVariables {
			if e.Name != "" && (e.IsRequired || e.IsSecret) {
				opts = append(opts, "--env", e.Name+"=<"+strings.ToLower(e.Name)+">")
			}
		}
		h.Add = strings.Join(append(append([]string{"/mcp add", slug}, opts...), cmd...), " ")
		return h
	}
	for _, r := range s.Remotes {
		if r.URL == "" || (r.Type != "streamable-http" && r.Type != "sse") {
			continue
		}
		parts := []string{"/mcp add", slug}
		if r.Type == "sse" {
			parts = append(parts, "--transport", "sse")
		}
		for _, hd := range r.Headers {
			if hd.Name == "" || !(hd.IsRequired || hd.IsSecret) {
				continue
			}
			v := placeholders.ReplaceAllString(orWord(hd.Value, "<value>"), "<$1>")
			if strings.ContainsAny(v, " \t") {
				// /mcp add splits on spaces; a value with one in it has to
				// go in the config by hand.
				h.Notes = append(h.Notes, fmt.Sprintf("needs the header %s: %s, which /mcp add cannot take on one line; add it to the entry in %s", hd.Name, v, "the config or .mcp.json"))
				continue
			}
			parts = append(parts, "--header", hd.Name+"="+v)
		}
		h.Add = strings.Join(append(parts, r.URL), " ")
		return h
	}
	h.Notes = append(h.Notes, "no npm, PyPI or container package and no remote URL: see "+orWord(s.WebsiteURL, "its registry entry"))
	return h
}

// placeholders are the {name} templates registry values use.
var placeholders = regexp.MustCompile(`\{([A-Za-z0-9_.-]+)\}`)

var nonSlug = regexp.MustCompile(`[^a-z0-9-]+`)

// slugOf is a short server name for /mcp add: the last part of the
// registry name, lower-cased, with anything but letters, digits and
// dashes turned into dashes.
func slugOf(name string) string {
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	s := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if s == "" {
		return "server"
	}
	return s
}

func orWord(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

// SearchResult is the /mcp search sheet.
func SearchResult(q string, hits []Hit) cmdresult.Result {
	if len(hits) == 0 {
		return cmdresult.Result{Notes: []string{fmt.Sprintf("the MCP registry has no server matching %q", q)}}
	}
	var b strings.Builder
	for i, h := range hits {
		if i > 0 {
			b.WriteString("\n")
		}
		title := h.Name
		if h.Title != "" && h.Title != h.Name {
			title = h.Title + "  (" + h.Name + ")"
		}
		fmt.Fprintf(&b, "%s\n", title)
		if h.Blurb != "" {
			fmt.Fprintf(&b, "  %s\n", textfmt.Clip(textfmt.OneLine(h.Blurb), 110))
		}
		if h.Add != "" {
			fmt.Fprintf(&b, "  %s\n", h.Add)
		}
		for _, n := range h.Notes {
			fmt.Fprintf(&b, "  note: %s\n", n)
		}
	}
	return cmdresult.Result{
		SheetTitle: fmt.Sprintf("MCP registry: %s for %q", textfmt.Plural(len(hits), "server"), q),
		Sheet:      strings.TrimRight(b.String(), "\n"),
		Tips:       []string{"nothing is installed until you run an /mcp add line; replace each <placeholder> first. Registry entries are published by their authors, not reviewed"},
	}
}
