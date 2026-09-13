package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/xmonader/janetcode/internal/mcp"
)

// authorizeMCP runs the OAuth flow for a remote server from the command
// line, printing the URL in case no browser can be opened.
func authorizeMCP(name string, global, project map[string]mcp.ServerConfig, tokens *mcp.TokenStore) error {
	cfg, ok := global[name]
	if !ok {
		cfg, ok = project[name]
	}
	if !ok {
		return fmt.Errorf("no MCP server named %q in the config or %s", name, mcp.ProjectFile)
	}
	if cfg.URL == "" {
		return fmt.Errorf("mcp %s runs locally; only remote servers use OAuth", name)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	_, err := mcp.Authorize(ctx, mcp.AuthOptions{
		ServerURL: cfg.URL, Store: tokens,
		ClientID: cfg.ClientID, ClientSecret: os.ExpandEnv(cfg.ClientSecret), Scope: cfg.Scope,
		OpenURL: mcp.OpenBrowser,
		Notify:  func(s string) { fmt.Fprintln(os.Stderr, "[mcp auth] "+s) },
	})
	if err != nil {
		return fmt.Errorf("mcp auth %s: %w", name, err)
	}
	fmt.Printf("mcp %s: authorized; token stored in %s\n", name, tokens.Path)
	return nil
}
