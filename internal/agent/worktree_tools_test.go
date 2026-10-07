package agent

import "testing"

// An editing subagent works on the notebooks in its checkout with the
// notebook tool, not by rewriting their JSON through write_file.
func TestWorktreeToolsIncludeNotebookEdit(t *testing.T) {
	w := &worktree{Dir: t.TempDir()}
	for _, tl := range w.tools(nil) {
		if tl.Name() == "notebook_edit" {
			return
		}
	}
	t.Fatal("an editing subagent's tools have no notebook_edit")
}
