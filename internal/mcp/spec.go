package mcp

import (
	"errors"
	"fmt"
	"strings"
)

// What "/mcp add <name> <command or url>" means: the words after the name
// are either a URL (a remote server) or a command line with its arguments
// (a local one). Options belong before the command, because everything
// after it is the command's own: "-y" in "npx -y pkg" is npx's, not ours.

// AddSpec is a parsed "/mcp add" line.
type AddSpec struct {
	Name string
	// From is the repository or directory given with --from; when it is
	// set Name and Config come from that repository's manifest instead.
	From string
	// Yes skips the confirmation a front end would otherwise ask for.
	Yes    bool
	Config ServerConfig
}

// ParseAdd reads the arguments of "/mcp add": the name, then the command
// or URL, with --env, --header, --transport, --from and --yes before it.
func ParseAdd(args []string) (AddSpec, error) {
	var spec AddSpec
	env, headers := map[string]string{}, map[string]string{}
	var words []string
	transport := ""
	i := 0
	for ; i < len(args); i++ {
		a := args[i]
		// After "--", and after the command itself, every word belongs
		// to the server's command line: options come before it.
		if a == "--" {
			words = append(words, args[i+1:]...)
			break
		}
		if len(words) >= 2 && !IsURL(words[1]) {
			words = append(words, a)
			continue
		}
		flag, inline, hasInline := strings.Cut(a, "=")
		value := func(what string) (string, error) {
			if hasInline {
				return inline, nil
			}
			if i+1 >= len(args) {
				return "", fmt.Errorf("%s needs %s", flag, what)
			}
			i++
			return args[i], nil
		}
		var err error
		switch flag {
		case "--env", "-e":
			var v string
			if v, err = value("KEY=value"); err == nil {
				err = putPair(env, v)
			}
		case "--header", "-H":
			var v string
			if v, err = value("Name: value"); err == nil {
				err = putPair(headers, v)
			}
		case "--from":
			spec.From, err = value("a git URL or a directory")
		case "--transport", "-t":
			transport, err = value("stdio, http or sse")
		case "--yes", "-y":
			spec.Yes = true
		default:
			if strings.HasPrefix(a, "-") && len(words) == 0 {
				err = fmt.Errorf("unknown option %q", a)
			} else {
				words = append(words, a)
			}
		}
		if err != nil {
			return spec, err
		}
	}
	switch transport {
	case "", "stdio", "http", "sse":
	default:
		return spec, fmt.Errorf("transport %q: use stdio, http or sse", transport)
	}
	if spec.From != "" {
		if len(words) > 0 {
			return spec, errors.New("--from installs what the repository declares: drop the command")
		}
		return spec, nil
	}
	if len(words) == 0 {
		return spec, errors.New("usage: /mcp add <name> <command or url> [--env K=V] [--header 'Name: value']")
	}
	spec.Name = words[0]
	if err := ValidServerName(spec.Name); err != nil {
		return spec, err
	}
	if len(words) == 1 {
		return spec, fmt.Errorf("/mcp add %s needs the command to run or the server's URL", spec.Name)
	}
	cfg, err := ParseTarget(words[1:])
	if err != nil {
		return spec, err
	}
	if transport != "" {
		if (transport == "stdio") != (cfg.URL == "") {
			return spec, fmt.Errorf("--transport %s does not match %q", transport, strings.Join(words[1:], " "))
		}
		cfg.Type = transport
	}
	if len(env) > 0 {
		if cfg.URL != "" {
			return spec, errors.New("--env is for a command; a remote server takes --header")
		}
		cfg.Env = env
	}
	if len(headers) > 0 {
		if cfg.URL == "" {
			return spec, errors.New("--header is for a remote server; a command takes --env")
		}
		cfg.Headers = headers
	}
	spec.Config = cfg
	return spec, nil
}

// ParseTarget turns the words after the name into a server: a URL makes a
// remote server, anything else a command with its arguments.
func ParseTarget(words []string) (ServerConfig, error) {
	if len(words) == 0 {
		return ServerConfig{}, errors.New("give the command to run or the server's URL")
	}
	if IsURL(words[0]) {
		if len(words) > 1 {
			return ServerConfig{}, errors.New("a URL takes no arguments; use --header for authentication")
		}
		return ServerConfig{URL: strings.TrimRight(words[0], "/")}, nil
	}
	if strings.HasPrefix(words[0], "-") {
		return ServerConfig{}, fmt.Errorf("unknown option %q", words[0])
	}
	cfg := ServerConfig{Command: words[0]}
	if len(words) > 1 {
		cfg.Args = words[1:]
	}
	return cfg, nil
}

// IsURL reports whether s names a remote server rather than a command.
func IsURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

// putPair records "KEY=value" or "Name: value" in m.
func putPair(m map[string]string, s string) error {
	if k, v, ok := strings.Cut(s, "="); ok && !strings.HasPrefix(k, " ") {
		if strings.TrimSpace(k) == "" {
			return fmt.Errorf("%q has no name", s)
		}
		m[strings.TrimSpace(k)] = v
		return nil
	}
	if k, v, ok := strings.Cut(s, ":"); ok {
		if strings.TrimSpace(k) == "" {
			return fmt.Errorf("%q has no name", s)
		}
		m[strings.TrimSpace(k)] = strings.TrimSpace(v)
		return nil
	}
	return fmt.Errorf("%q is not KEY=value", s)
}

// Describe summarises a server the way /mcp add reports what it wrote.
func Describe(cfg ServerConfig) string {
	var parts []string
	if cfg.URL != "" {
		parts = append(parts, cfg.URL)
	} else {
		parts = append(parts, strings.TrimSpace(cfg.Command+" "+strings.Join(cfg.Args, " ")))
	}
	if cfg.Cwd != "" {
		parts = append(parts, "in "+cfg.Cwd)
	}
	if n := len(cfg.Env); n > 0 {
		parts = append(parts, plural(n, "env var"))
	}
	if n := len(cfg.Headers); n > 0 {
		parts = append(parts, plural(n, "header"))
	}
	return strings.Join(parts, " · ")
}

func plural(n int, what string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", what)
	}
	return fmt.Sprintf("%d %ss", n, what)
}
