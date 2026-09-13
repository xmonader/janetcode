package webui

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/textfmt"
	"github.com/xmonader/janetcode/internal/tool"
)

func (s *server) agentAPI(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	task := s.taskTool()
	if task == nil {
		http.Error(w, "no task tool", http.StatusNotFound)
		return
	}
	log, ok := task.ChildLog(id)
	if !ok {
		http.Error(w, "unknown subagent", http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]any{
		"id":    log.ID,
		"desc":  log.Description,
		"done":  log.Done,
		"last":  log.Last,
		"edit":  log.Editing,
		"steps": log.Steps(),
		"text":  formatChildLog(log),
	})
}

func (s *server) jobAPI(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.URL.Query().Get("id"))
	if err != nil || id < 1 {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	if s.opts.Jobs == nil {
		http.Error(w, "no jobs", http.StatusNotFound)
		return
	}
	job, ok := s.opts.Jobs.Get(id)
	if !ok {
		http.Error(w, "unknown job", http.StatusNotFound)
		return
	}
	out, err := s.opts.Jobs.Output(id, 200)
	if err != nil {
		out = err.Error()
	}
	writeJSON(w, map[string]any{
		"id":      job.ID,
		"command": job.Command,
		"done":    job.Done,
		"output":  out,
	})
}

func (s *server) ptcAPI(w http.ResponseWriter, r *http.Request) {
	runs := s.ptcRuns()
	if n, err := strconv.Atoi(r.URL.Query().Get("n")); err == nil && n > 0 {
		if n > len(runs) {
			http.Error(w, "unknown run", http.StatusNotFound)
			return
		}
		writeJSON(w, ptcDetail(runs[n-1]))
		return
	}
	items := make([]ptcItem, 0, len(runs))
	for _, run := range runs {
		items = append(items, ptcSummary(run))
	}
	writeJSON(w, map[string]any{"runs": items})
}

func (s *server) ptcRuns() []agent.PTCRun {
	if s.opts.Agent == nil || s.opts.Agent.Log == nil {
		return nil
	}
	return agent.FoldPTC(s.opts.Agent.Log.Events())
}

type ptcItem struct {
	N       int    `json:"n"`
	Tool    string `json:"tool"`
	Summary string `json:"summary"`
	Failed  bool   `json:"failed"`
	Calls   int    `json:"calls"`
}

func ptcSummary(r agent.PTCRun) ptcItem {
	sum := r.Tool
	if d := ptcLabel(r.Code); d != "" {
		sum = d
	}
	return ptcItem{N: r.N, Tool: r.Tool, Summary: sum, Failed: r.Failed, Calls: len(r.Calls)}
}

func ptcDetail(r agent.PTCRun) map[string]any {
	calls := make([]map[string]any, 0, len(r.Calls))
	for _, c := range r.Calls {
		calls = append(calls, map[string]any{
			"name":   c.Name,
			"input":  c.Input,
			"output": c.Output,
			"failed": c.Failed,
		})
	}
	return map[string]any{
		"n":      r.N,
		"tool":   r.Tool,
		"code":   r.Code,
		"result": r.Result,
		"failed": r.Failed,
		"calls":  calls,
		"text":   formatPTC(r),
	}
}

func ptcLabel(code string) string {
	code = strings.TrimSpace(code)
	if code == "" {
		return ""
	}
	if i := strings.IndexByte(code, '\n'); i > 0 {
		code = code[:i]
	}
	return textfmt.Clip(textfmt.OneLine(code), 80)
}

func formatPTC(r agent.PTCRun) string {
	var b strings.Builder
	state := "finished"
	switch {
	case r.Failed:
		state = "failed"
	case r.Result == "":
		state = "running"
	}
	b.WriteString(r.Tool + " " + strconv.Itoa(r.N) + " (" + state + ")\n")
	if r.Code != "" {
		b.WriteString(strings.TrimRight(r.Code, "\n") + "\n")
	}
	for _, c := range r.Calls {
		b.WriteString("→ " + c.Name + " " + textfmt.OneLine(c.Input) + "\n")
		if c.Output != "" {
			b.WriteString(tool.TruncateTo(c.Output, 1500) + "\n")
		}
	}
	if r.Result != "" {
		b.WriteString(strings.TrimRight(r.Result, "\n") + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
