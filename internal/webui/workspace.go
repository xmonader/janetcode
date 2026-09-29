package webui

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/present"
	"github.com/xmonader/janetcode/internal/prompt"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/textfmt"
	"github.com/xmonader/janetcode/internal/tool"
)

type workspace struct {
	Todos     []todoItem  `json:"todos"`
	Files     []fileItem  `json:"files"`
	Jobs      []jobItem   `json:"jobs"`
	Agents    []agentItem `json:"agents"`
	Memory    []memItem   `json:"memory"`
	CanUndo   bool        `json:"can_undo"`
	CanRedo   bool        `json:"can_redo"`
	Cost      string      `json:"cost"`
	CtxPct    int         `json:"ctx_pct"`
	CtxLabel  string      `json:"ctx_label"`
	Yolo      bool        `json:"yolo"`
	Auto      bool        `json:"auto"`
	Edits     bool        `json:"edits"`
	Plan      bool        `json:"plan"`
	ReadOnly  bool        `json:"readonly"`
	Mode      string      `json:"mode"`
	Terminals []termItem  `json:"terminals,omitempty"`
	Ptc       []ptcItem   `json:"ptc,omitempty"`
	Branch    string      `json:"branch,omitempty"`
}

type termItem struct {
	ID   int    `json:"id"`
	Last string `json:"last"`
	Done bool   `json:"done"`
}

type todoItem struct {
	Text   string `json:"text"`
	Status string `json:"status"`
}

type fileItem struct {
	Seq  int    `json:"seq"`
	Path string `json:"path"`
	Tool string `json:"tool"`
	Add  int    `json:"add"`
	Del  int    `json:"del"`
	Turn int    `json:"turn,omitempty"`
}

type jobItem struct {
	ID      int    `json:"id"`
	Command string `json:"command"`
	Done    bool   `json:"done"`
	Summary string `json:"summary"`
}

type agentItem struct {
	ID   string `json:"id"`
	Desc string `json:"desc"`
	Done bool   `json:"done"`
	Last string `json:"last"`
	Edit bool   `json:"edit"`
}

func sessionMode(ag *agent.Agent) string {
	if ag == nil {
		return "ask"
	}
	switch modes := ag.Settings(); {
	case modes.PlanMode:
		return "PLAN"
	case modes.ReadOnly:
		return "READ-ONLY"
	case modes.AutoApprove:
		return "YOLO"
	case modes.AutoMode:
		return "AUTO"
	case modes.AutoEdits:
		return "EDITS"
	default:
		return "ask"
	}
}

func termsOf(ag *agent.Agent) *tool.Terms {
	if ag == nil || ag.Tools == nil {
		return nil
	}
	tl, ok := ag.Tools.Get("terminal")
	if !ok {
		return nil
	}
	switch t := tl.(type) {
	case tool.TermTool:
		return t.Terms
	case *tool.TermTool:
		return t.Terms
	}
	return nil
}

func (s *server) taskTool() *agent.Task { return s.opts.Agent.TaskTool() }

type memItem struct {
	Name string `json:"name"`
	Text string `json:"text"`
}

func (s *server) workspaceAPI(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.workspace())
}

func (s *server) workspace() workspace {
	ws := workspace{}
	ag := s.opts.Agent
	if ag == nil {
		return ws
	}
	for _, t := range ag.Todos() {
		st := t.Status
		if st == "" {
			st = "pending"
		}
		ws.Todos = append(ws.Todos, todoItem{Text: t.Text, Status: st})
	}
	for _, c := range ag.Changes() {
		if c.Undone {
			continue
		}
		add, del := c.Stats()
		path := c.Path
		if s.cwd() != "" {
			path = textfmt.RelTo(s.cwd(), path)
		}
		ws.Files = append(ws.Files, fileItem{Seq: c.Seq, Path: path, Tool: c.Tool, Add: add, Del: del, Turn: c.Turn})
	}
	if s.opts.Jobs != nil {
		for _, j := range s.opts.Jobs.List() {
			ws.Jobs = append(ws.Jobs, jobItem{ID: j.ID, Command: j.Command, Done: j.Done, Summary: j.Summary()})
		}
	}
	if task := s.taskTool(); task != nil {
		for _, c := range task.Children() {
			ws.Agents = append(ws.Agents, agentItem{ID: c.ID, Desc: c.Description, Done: c.Done, Last: c.Last, Edit: c.Editing})
		}
	}
	if s.opts.Memory != nil {
		for _, e := range s.opts.Memory.Entries() {
			text := e.Description
			if text == "" {
				text = e.Name
			}
			ws.Memory = append(ws.Memory, memItem{Name: e.Name, Text: text})
		}
	}
	ws.CanUndo = len(ag.Checkpoints()) > 0
	modes := ag.Settings()
	ws.Yolo = modes.AutoApprove
	ws.Auto = modes.AutoMode
	ws.Edits = modes.AutoEdits
	ws.Plan = modes.PlanMode
	ws.ReadOnly = modes.ReadOnly
	ws.Mode = sessionMode(ag)
	if terms := termsOf(ag); terms != nil {
		for _, x := range terms.List() {
			ws.Terminals = append(ws.Terminals, termItem{ID: x.ID, Last: x.Last, Done: x.Done})
		}
	}
	if ag.Log != nil {
		for _, run := range agent.FoldPTC(ag.Log.Events()) {
			ws.Ptc = append(ws.Ptc, ptcSummary(run))
		}
	}
	if usd, _, _, ok := ag.SessionCost(); ok {
		ws.Cost = llm.FormatUSD(usd)
	}
	if b, ok := prompt.GitBranch(s.cwd()); ok && b != "" {
		ws.Branch = b
	}
	est := ag.EstimateTokens()
	if lim := ag.CompactThreshold(); lim > 0 {
		ws.CtxPct = est * 100 / lim
		if ws.CtxPct > 100 {
			ws.CtxPct = 100
		}
		ws.CtxLabel = fmt.Sprintf("%s / %s", textfmt.Tokens(est), textfmt.Tokens(lim))
	}
	return ws
}

func (s *server) changeAPI(w http.ResponseWriter, r *http.Request) {
	n, _ := strconv.Atoi(r.URL.Query().Get("n"))
	if n < 1 || s.opts.Agent == nil {
		http.Error(w, "unknown change", http.StatusNotFound)
		return
	}
	c, err := s.opts.Agent.Change(n)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	path := c.Path
	if s.cwd() != "" {
		path = textfmt.RelTo(s.cwd(), path)
	}
	writeJSON(w, map[string]any{
		"seq":  c.Seq,
		"path": path,
		"tool": c.Tool,
		"diff": formatHunks(c),
	})
}

func formatHunks(c agent.Change) string {
	var sb strings.Builder
	hunks := c.Hunks()
	if len(hunks) == 0 {
		if len(c.After) > 0 {
			return string(c.After)
		}
		return ""
	}
	for _, h := range hunks {
		fmt.Fprintf(&sb, "@@ -%d,%d +%d,%d @@\n", h.OldStart, h.OldCount, h.NewStart, h.NewCount)
		for _, line := range h.Lines {
			sb.WriteString(line)
			if !strings.HasSuffix(line, "\n") {
				sb.WriteByte('\n')
			}
		}
	}
	return sb.String()
}

func (s *server) newChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	s.mu.Lock()
	running := s.running
	s.mu.Unlock()
	if running {
		http.Error(w, "wait for the current turn to finish", http.StatusConflict)
		return
	}
	if err := s.startNewSession(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"ok": "1", "session": s.sess().Meta.ID})
}

func (s *server) startNewSession() error {
	dir, err := session.Dir()
	if err != nil {
		return err
	}
	model, provider := "", s.provider()
	if s.opts.Agent != nil {
		model = s.opts.Agent.Settings().Model
	}
	sess, err := session.New(dir, s.cwd(), provider, model)
	if err != nil {
		return err
	}
	s.setSession(sess)
	s.opts.CD.SetSession(sess)
	if s.opts.Agent != nil {
		if err := s.opts.Agent.OpenSession(sess, nil); err != nil {
			return err
		}
	}
	s.resetView()
	return nil
}

func (s *server) resetView() {
	s.mu.Lock()
	s.entries = nil
	s.nextID = 0
	s.log = nil
	s.queued = nil
	s.status = ""
	p, a := s.pending, s.asking
	s.pending, s.asking = nil, nil
	s.mu.Unlock()
	s.emit(envelope{Type: "reset"})
	s.emit(envelope{Type: "hello", Hello: ptr(s.hello())})
	// A prompt still up (a background subagent's) is refused, not
	// dropped: its caller holds the one prompt slot and would keep it,
	// and every prompt after it would wait behind it. Questions likewise
	// hold theirs. Refused after the reset is sent, so a prompt waiting
	// behind is not wiped off the page by it.
	if p != nil {
		select {
		case p.reply <- verdict{d: agent.Deny, reason: agent.CancelledReason}:
		default:
		}
	}
	if a != nil {
		select {
		case a.reply <- nil:
		default:
		}
	}
}

func (s *server) loadSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	running := s.running
	s.mu.Unlock()
	if running {
		http.Error(w, "wait for the current turn to finish", http.StatusConflict)
		return
	}
	dir, err := session.Dir()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	sess, msgs, err := session.Open(dir, body.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if s.cwd() != "" && sess.Meta.Cwd != "" && sess.Meta.Cwd != s.cwd() {
		http.Error(w, "that session belongs to a different directory", http.StatusBadRequest)
		return
	}
	s.setSession(sess)
	s.opts.CD.SetSession(sess)
	if s.opts.Agent != nil {
		if err := s.opts.Agent.OpenSession(sess, append([]llm.Message(nil), msgs...)); err != nil {
			s.errf("%v", err)
		}
	}
	s.resetView()
	s.replayHistory(msgs)
	s.emitStatus()
	writeJSON(w, map[string]string{"ok": "1", "session": sess.Meta.ID})
}

// replayHistory rebuilds the thread view from a loaded session. Tool calls
// are paired with their results; results that never arrived are closed out.
func (s *server) replayHistory(msgs []llm.Message) {
	// Tool-call id → the id of the thread entry showing it, so the
	// matching result can close it out. The entries themselves belong to
	// the server's lock, not to this loop.
	open := map[string]string{}
	inputs := map[string]string{}
	for _, m := range msgs {
		for _, b := range m.Blocks {
			switch b.Type {
			case llm.BlockText:
				if m.Role == llm.RoleUser {
					// Harness attachments (the repository map, skills,
					// recalled memory) ride along as user text; they are
					// context, not something the user typed.
					if b.Synthetic || strings.HasPrefix(b.Text, "[repository map:") {
						continue
					}
					s.add(entry{Kind: "user", Text: b.Text, Done: true})
				} else if strings.TrimSpace(b.Text) != "" {
					s.add(entry{Kind: "assistant", Text: b.Text, Done: true})
				}
			case llm.BlockThinking:
				if strings.TrimSpace(b.Text) != "" {
					s.add(entry{Kind: "thinking", Text: b.Text, Done: true})
				}
			case llm.BlockToolUse:
				open[b.ID] = s.add(entry{Kind: "tool", Name: b.Name, CallID: b.ID, Running: true,
					Summary: present.ToolSummary(b.Name, string(b.Input), 80, s.cwd())})
				inputs[b.ID] = string(b.Input)
			case llm.BlockToolResult:
				id, ok := open[b.ToolUseID]
				if !ok {
					continue
				}
				delete(open, b.ToolUseID)
				s.update(id, func(e *entry) {
					// Read the way the live row was: a failing test run
					// keeps its failure, a missing file is an answer.
					end := present.EndOf(e.Name, inputs[b.ToolUseID], b.Text, b.IsError, 0)
					e.Running = false
					e.Done = true
					e.Error = end.Failed
					e.Output = textfmt.Clip(end.Output, 4000)
				})
			}
		}
	}
	// Calls whose result never made it into the session: show them as
	// finished rather than for ever running.
	for _, id := range open {
		s.update(id, func(e *entry) {
			e.Running = false
			e.Done = true
		})
	}
}

func (s *server) upload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if s.cwd() == "" {
		http.Error(w, "no working directory", http.StatusConflict)
		return
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, "bad upload", http.StatusBadRequest)
		return
	}
	dir := filepath.Join(s.cwd(), ".janetcode", "uploads")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var paths []string
	for _, fh := range r.MultipartForm.File["files"] {
		if fh.Size > 16<<20 {
			http.Error(w, fh.Filename+" is over 16MB", http.StatusRequestEntityTooLarge)
			return
		}
		name := filepath.Base(fh.Filename)
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
			http.Error(w, "bad file name", http.StatusBadRequest)
			return
		}
		src, err := fh.Open()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		dst, out, err := createUpload(dir, name)
		if err == nil {
			_, err = io.Copy(out, src)
		}
		src.Close()
		if err != nil {
			if out != nil {
				out.Close()
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out.Close()
		paths = append(paths, textfmt.RelTo(s.cwd(), dst))
	}
	writeJSON(w, map[string]any{"ok": "1", "paths": paths})
}

// createUpload makes a new file for an upload named name in dir, stamped
// with the time. It never opens one that exists: two files with one name
// in one second (shot.png from two folders) used to land on one path, the
// second overwriting the first, so they get a number instead.
func createUpload(dir, name string) (string, *os.File, error) {
	stamp := time.Now().Unix()
	for i := 0; ; i++ {
		dst := filepath.Join(dir, fmt.Sprintf("%d-%s", stamp, name))
		if i > 0 {
			dst = filepath.Join(dir, fmt.Sprintf("%d-%d-%s", stamp, i, name))
		}
		f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err == nil || !os.IsExist(err) || i >= 100 {
			return dst, f, err
		}
	}
}
