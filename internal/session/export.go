package session

import (
	"fmt"
	"strings"

	"github.com/xmonader/janetcode/internal/llm"
)

// Markdown renders a conversation as a shareable transcript: user and
// assistant text as sections, tool calls as their name and input, tool
// results as fenced blocks trimmed to keep the file readable. Nothing is
// dropped silently: trimmed results say how much was cut.
func Markdown(meta Meta, msgs []llm.Message) string {
	const maxResult = 2000
	var sb strings.Builder
	fmt.Fprintf(&sb, "# janetcode session %s\n\n", meta.ID)
	fmt.Fprintf(&sb, "- Directory: %s\n- Provider: %s / %s\n- Started: %s\n\n", meta.Cwd, meta.Provider, meta.Model, meta.Created.Local().Format("2006-01-02 15:04"))
	for _, m := range msgs {
		for _, b := range m.Blocks {
			switch b.Type {
			case llm.BlockText:
				if m.Role == llm.RoleUser {
					sb.WriteString("## User\n\n")
				} else {
					sb.WriteString("## Assistant\n\n")
				}
				sb.WriteString(strings.TrimSpace(b.Text) + "\n\n")
			case llm.BlockThinking:
				if t := strings.TrimSpace(b.Text); t != "" {
					sb.WriteString("<details><summary>Thinking</summary>\n\n" + t + "\n\n</details>\n\n")
				}
			case llm.BlockToolUse:
				fmt.Fprintf(&sb, "**Tool call** `%s`\n\n```json\n%s\n```\n\n", b.Name, strings.TrimSpace(string(b.Input)))
			case llm.BlockToolResult:
				label := "Result"
				if b.IsError {
					label = "Error"
				}
				text := strings.TrimSpace(b.Text)
				if len(text) > maxResult {
					text = text[:maxResult] + fmt.Sprintf("\n… [%d more bytes]", len(text)-maxResult)
				}
				fmt.Fprintf(&sb, "**%s**\n\n```\n%s\n```\n\n", label, strings.ReplaceAll(text, "```", "'''"))
			case llm.BlockImage:
				sb.WriteString("*(image attached)*\n\n")
			}
		}
	}
	return sb.String()
}
