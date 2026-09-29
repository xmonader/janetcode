package webui

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/session"
)

func (s *server) inspectAPI(w http.ResponseWriter, r *http.Request) {
	if s.opts.Inspect == nil {
		http.Error(w, "nothing is registered for inspection", http.StatusNotFound)
		return
	}
	q := r.URL.Query().Get("q")
	if q == "" {
		var list []map[string]string
		for _, sub := range s.opts.Inspect.Subjects() {
			list = append(list, map[string]string{"name": sub.Name, "summary": sub.Summary, "usage": sub.Usage})
		}
		writeJSON(w, list)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	out, err := s.opts.Inspect.Report(ctx, q)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]string{"q": q, "body": out})
}

func (s *server) sessionsAPI(w http.ResponseWriter, r *http.Request) {
	dir, err := session.Dir()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	infos, err := session.List(dir, s.cwd())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	cur := ""
	if s.sess() != nil {
		cur = s.sess().Meta.ID
	}
	type row struct {
		ID       string `json:"id"`
		Label    string `json:"label"`
		Title    string `json:"title"`
		Messages int    `json:"messages"`
		Updated  string `json:"updated"`
		Current  bool   `json:"current"`
		Settled  bool   `json:"settled"`
		// Depth, Prefix and Note carry the fork lineage (see
		// session.Lineage): a fork is listed under the session it was
		// copied from, and one whose parent is not in this listing says so.
		Depth  int    `json:"depth"`
		Prefix string `json:"prefix"`
		Note   string `json:"note,omitempty"`
		// Hit is the line of the conversation that matched ?q=, and
		// Turn the turn it is in: the list filter searches what was
		// said, not only the titles.
		Hit  string `json:"hit,omitempty"`
		Turn int    `json:"turn,omitempty"`
	}
	hits := map[string]session.Hit{}
	if q := strings.TrimSpace(r.URL.Query().Get("q")); q != "" {
		found, err := session.Search(dir, s.cwd(), q, 0)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		for _, h := range found {
			hits[h.Info.Meta.ID] = h
		}
	}
	nodes := session.Lineage(infos, cur)
	out := make([]row, 0, len(nodes))
	for _, n := range nodes {
		in := n.Info
		out = append(out, row{
			ID: in.Meta.ID, Label: in.Label(), Title: in.Meta.Title,
			Messages: in.Messages, Updated: in.Updated.Format(time.RFC3339),
			Current: n.Current, Settled: in.Meta.Settled,
			Depth: n.Depth, Prefix: n.Prefix(), Note: n.Note(),
			Hit: hits[in.Meta.ID].Snippet, Turn: hits[in.Meta.ID].Turn,
		})
	}
	writeJSON(w, out)
}

func (s *server) panelAPI(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	var title, body string
	switch name {
	case "context":
		title, body = "context", s.contextText()
	case "stats":
		title, body = "stats", s.statsText()
	case "perf":
		title, body = "perf", s.perfText()
	case "usage":
		title, body = "usage", s.usageText()
	case "todos":
		title, body = "todos", s.todosText()
	case "jobs":
		title, body = "jobs", s.jobsText()
	case "memory":
		title, body = "memory", s.memoryText("")
	case "skills":
		title, body = "skills", s.skillsText()
	case "changes":
		title, body = "changes", s.changesText("")
	case "permissions":
		title, body = "permissions", s.permissionsText()
	default:
		http.Error(w, "unknown panel", http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]string{"name": name, "title": title, "body": body})
}
