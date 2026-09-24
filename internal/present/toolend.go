package present

import (
	"fmt"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/shell"
	"github.com/xmonader/janetcode/internal/textfmt"
	"github.com/xmonader/janetcode/internal/tool"
)

// ToolEnd is how a finished tool call reads on its row.
type ToolEnd struct {
	// Output is what the row shows, without lines meant for the model.
	Output string
	// Failed marks the row as an error: the tool said so, or a command
	// exited non-zero where that is a failure.
	Failed bool
	// Label is the row's trailer: "1.2s", or "exit 1 · 1.2s" for a
	// command that failed.
	Label string
}

// EndOf says how a tool call ended. The bash tool reports a non-zero exit
// in its output, not as an error, so a failing test run drew a green ✓
// in one front-end and "Done" in the other; the row says how the command
// ended, and the "[exit code N]" line, which is for the model, is not shown.
// A search that found nothing exits 1 and is an answer, not a failure;
// tool.CommandFailed knows which commands those are.
func EndOf(name, input, output string, isError bool, took time.Duration) ToolEnd {
	end := ToolEnd{Output: output, Failed: isError}
	if took > 0 {
		end.Label = textfmt.Duration(took)
	}
	if isError && (name == "read_file" || name == "list_dir") {
		// A lookup of a path that is not there is an answer, like a grep
		// that matched nothing; the red ✗ made the model's routine
		// "does this exist?" read like something had broken.
		if msg := strings.TrimPrefix(output, "Error: "); strings.HasSuffix(strings.TrimSpace(msg), " "+tool.NotFound) {
			end.Output, end.Failed = msg, false
		}
		return end
	}
	if name != "bash" {
		return end
	}
	code, failed := tool.CommandFailed(shell.CommandOf(input), output)
	end.Output = tool.WithoutExitLine(output)
	if failed {
		end.Failed = true
		if end.Label == "" {
			end.Label = fmt.Sprintf("exit %d", code)
		} else {
			end.Label = fmt.Sprintf("exit %d · %s", code, end.Label)
		}
	}
	return end
}
