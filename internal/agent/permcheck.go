package agent

import (
	"encoding/json"
	"strings"

	"github.com/xmonader/janetcode/internal/perm"
	"github.com/xmonader/janetcode/internal/present"
)

// PermissionCheck is /permissions check, in the terminal and the web
// alike: what the rules decide about a call and why, before anything
// runs. The argument is a bash command, or a tool name followed by its
// JSON input (read_file {"path":".env"}).
func (a *Agent) PermissionCheck(args []string) string {
	if len(args) == 0 {
		return "check: give a command, or a tool name and its JSON input"
	}
	tool, input := "bash", ""
	if len(args) >= 2 && strings.HasPrefix(strings.TrimSpace(args[1]), "{") {
		tool, input = args[0], strings.Join(args[1:], " ")
	} else {
		b, _ := json.Marshal(map[string]string{"command": strings.Join(args, " ")})
		input = string(b)
	}
	if !json.Valid([]byte(input)) {
		return "check: the input after the tool name is not JSON"
	}
	root := a.RootDir()
	c := perm.Call{Tool: tool, Input: json.RawMessage(input), Root: root}
	c.Subject = perm.SubjectIn(tool, c.Input, root)
	return tool + " " + present.ToolSummary(tool, input, 80, root) + ": " + a.RulesSnapshot().Explain(c)
}
