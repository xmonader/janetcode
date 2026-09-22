package webui

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/xmonader/janetcode/internal/mermaid"
)

// mermaidAPI draws a ```mermaid fence with the same subset the terminal
// uses. ok is false when the source is not in that subset; the page keeps
// the fence as code.
func (s *server) mermaidAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Src string `json:"src"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	lines, ok := mermaid.Render(body.Src, 72)
	writeJSON(w, map[string]any{"ok": ok, "lines": lines})
}
