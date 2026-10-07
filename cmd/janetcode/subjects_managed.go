package main

import (
	"fmt"
	"strings"

	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/managed"
)

// managedReport is --inspect managed: the managed policy in force, where
// it came from, and what it fixes for this session.
func managedReport(cfg *config.Config) string {
	var pol *managed.Policy
	if cfg != nil {
		pol = cfg.Managed
	}
	if pol == nil {
		return "no managed policy (" + managed.SystemPath() + " does not exist and " + managed.EnvFile + " is unset)"
	}
	var b strings.Builder
	for _, l := range pol.Summary() {
		b.WriteString(l + "\n")
	}
	for _, kind := range []struct {
		name  string
		rules []string
	}{{"allow", pol.Permissions.Allow}, {"deny", pol.Permissions.Deny}, {"ask", pol.Permissions.Ask}} {
		for _, r := range kind.rules {
			fmt.Fprintf(&b, "  %-5s %s\n", kind.name, r)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
