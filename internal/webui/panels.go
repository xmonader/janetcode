package webui

import (
	"context"
	"net/http"
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
	infos, err := session.List(dir, s.opts.Cwd)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	cur := ""
	if s.opts.Session != nil {
		cur = s.opts.Session.Meta.ID
	}
	type row struct {
		ID       string `json:"id"`
		Label    string `json:"label"`
		Title    string `json:"title"`
		Messages int    `json:"messages"`
		Updated  string `json:"updated"`
		Current  bool   `json:"current"`
		Settled  bool   `json:"settled"`
	}
	out := make([]row, 0, len(infos))
	for _, in := range infos {
		out = append(out, row{
			ID: in.Meta.ID, Label: in.Label(), Title: in.Meta.Title,
			Messages: in.Messages, Updated: in.Updated.Format(time.RFC3339),
			Current: in.Meta.ID == cur, Settled: in.Meta.Settled,
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
