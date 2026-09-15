package mcp

import (
	"regexp"
	"strings"
)

var badChars = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

// ToolName builds the registry name: providers only accept [A-Za-z0-9_-]
// and at most 64 characters.
func ToolName(server, tool string) string {
	n := "mcp__" + badChars.ReplaceAllString(server, "_") + "__" + badChars.ReplaceAllString(tool, "_")
	if len(n) > 64 {
		n = n[:64]
	}
	return strings.TrimRight(n, "_-")
}
