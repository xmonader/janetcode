// Package webui serves the session in a browser: the same agent as the TUI,
// streamed over SSE, with permission prompts answered from the page.
package webui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/feature"
	"github.com/xmonader/janetcode/internal/flavor"
	"github.com/xmonader/janetcode/internal/inspect"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/memory"
	"github.com/xmonader/janetcode/internal/prompt"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/textfmt"
	"github.com/xmonader/janetcode/internal/tool"
	"github.com/xmonader/janetcode/internal/weburl"
)

// Options configures the browser UI.
type Options struct {
	Agent    *agent.Agent
	Session  *session.Session
	Provider string
	Cwd      string
	Version  string
	// Addr is the listen address. Empty means 127.0.0.1:8787.
	Addr string
	// Opened reports the URL once the listener is bound (tests, logs).
	Opened func(url string)

	Inspect   *inspect.Registry
	Memory    *memory.Store
	Jobs      *tool.Jobs
	Features  *feature.Catalog
	ConfigDir string
	// Workspace is the shared host file tools use; /ssh and /docker retarget it.
	Workspace *tool.Workspace

	// Typed collects hosts the user names in their own messages so
	// fetch_url may reach them; @path attachments are expanded with it.
	Typed      *weburl.Set
	Config     *config.Config
	ConfigPath string
	// Models switches the session onto another model's provider.
	Models func(string) (llm.Provider, string, error)
	// MCP describes the MCP servers, for /mcp.
	MCP *MCPOptions
	// Doctor is the same checkup janetcode --doctor prints.
	Doctor func(io.Writer) error
}

const defaultAddr = "127.0.0.1:8787"

// ListenAddr fills in a missing host so --web 8787 and --web :8787 both
// bind loopback. An explicit host is honoured as given.
func ListenAddr(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return defaultAddr
	}
	if !strings.Contains(s, ":") {
		return "127.0.0.1:" + s
	}
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		return s
	}
	if host == "" {
		return "127.0.0.1:" + port
	}
	return s
}

// Loopback reports whether addr binds only the local machine.
func Loopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Run listens on opts.Addr and serves until ctx ends.
func Run(ctx context.Context, opts Options) error {
	addr := ListenAddr(opts.Addr)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	url := "http://" + displayAddr(ln.Addr())
	if opts.Opened != nil {
		opts.Opened(url)
	} else {
		fmt.Fprintf(os.Stderr, "janetcode %s serving %s (%s/%s in %s)\n",
			opts.Version, url, opts.Provider, opts.Agent.Settings().Model, opts.Cwd)
		if !Loopback(ln.Addr().String()) {
			fmt.Fprintln(os.Stderr, "warning: bound off loopback; anyone who can reach this port can approve commands in this directory")
		}
	}
	s := newServer(ctx, opts)
	hs := &http.Server{Handler: s.handler()}
	go func() {
		<-ctx.Done()
		shctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = hs.Shutdown(shctx)
	}()
	err = hs.Serve(ln)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func displayAddr(a net.Addr) string {
	ta, ok := a.(*net.TCPAddr)
	if !ok {
		return a.String()
	}
	if ta.IP == nil || ta.IP.IsUnspecified() {
		return fmt.Sprintf("127.0.0.1:%d", ta.Port)
	}
	return ta.String()
}

type server struct {
	opts Options
	// life ends when Run's context does. Every turn, background command
	// and subagent this server starts derives its context from it, so
	// shutting the server down stops them rather than leaving them
	// running against a server nobody can reach.
	life context.Context

	mu      sync.Mutex
	seq     int
	log     []envelope
	subs    map[chan envelope]struct{}
	entries []*entry
	nextID  int
	running bool
	phase   string
	started time.Time
	cancel  context.CancelFunc
	queued  []string
	pending *permReq
	asking  *askReq
	status  string
	verbose bool
}

type envelope struct {
	Seq       int              `json:"seq"`
	Type      string           `json:"type"`
	Entry     *entry           `json:"entry,omitempty"`
	Status    *statusPayload   `json:"status,omitempty"`
	Perm      *permPayload     `json:"permission,omitempty"`
	Hello     *helloPayload    `json:"hello,omitempty"`
	Notice    string           `json:"notice,omitempty"`
	Error     string           `json:"error,omitempty"`
	Questions []agent.Question `json:"questions,omitempty"`
}

type helloPayload struct {
	Cwd      string `json:"cwd"`
	Model    string `json:"model"`
	Provider string `json:"provider"`
	Session  string `json:"session"`
	Title    string `json:"title"`
	Version  string `json:"version"`
	Yolo     bool   `json:"yolo"`
	Auto     bool   `json:"auto"`
	Edits    bool   `json:"edits"`
	Plan     bool   `json:"plan"`
	ReadOnly bool   `json:"readonly"`
	Mode     string `json:"mode"`
}

type statusPayload struct {
	Running  bool   `json:"running"`
	Phase    string `json:"phase"`
	Elapsed  string `json:"elapsed,omitempty"`
	CtxPct   int    `json:"ctx_pct"`
	Queued   int    `json:"queued"`
	Yolo     bool   `json:"yolo"`
	Auto     bool   `json:"auto"`
	Edits    bool   `json:"edits"`
	Plan     bool   `json:"plan"`
	ReadOnly bool   `json:"readonly"`
	Mode     string `json:"mode"`
	Verbose  bool   `json:"verbose"`
	Status   string `json:"status,omitempty"`
	Cost     string `json:"cost,omitempty"`
}

type permPayload struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Summary    string `json:"summary"`
	Preview    string `json:"preview,omitempty"`
	Warning    string `json:"warning,omitempty"`
	Rule       string `json:"rule,omitempty"`
	Expandable bool   `json:"expandable,omitempty"`
	// Rungs is the ladder of rules the "remember this rule" button may
	// save, narrowest first, and RuleIndex the one it starts on — the
	// same choice the terminal offers with its arrow keys (see
	// perm.SuggestLadder). One rung means there is nothing to choose.
	Rungs     []permRung `json:"rungs,omitempty"`
	RuleIndex int        `json:"ruleIndex,omitempty"`
	// Learned is the ledger's note that this call's shape has been
	// approved several times with no refusal (see agent.ToolCall), shown
	// next to Rule so the "remember this rule" button explains itself.
	Learned string `json:"learned,omitempty"`
	// Impact is the reach of an edit_file or write_file call to an
	// existing file — the symbols it redefines, their references, their
	// tests, and the checks that will run (see package impact) — carried
	// verbatim from agent.ToolCall.Impact. "" for every other call and for
	// a new file.
	Impact string `json:"impact,omitempty"`
}

// permRung is one choosable rule on a prompt's ladder, with the ledger's
// note about that exact rule.
type permRung struct {
	Rule    string `json:"rule"`
	Learned string `json:"learned,omitempty"`
}

func newServer(life context.Context, opts Options) *server {
	s := &server{
		opts: opts,
		life: life,
		subs: map[chan envelope]struct{}{},
	}
	if opts.Agent != nil {
		opts.Agent.Approver = s
		opts.Agent.Asker = s
		if opts.Agent.Cron != nil {
			opts.Agent.Cron.OnFire = func(id, prompt string) {
				s.cronFire(id, prompt)
			}
		}
	}
	return s
}

func (s *server) cronFire(id, prompt string) {
	s.mu.Lock()
	running := s.running
	s.mu.Unlock()
	if running {
		s.notice("loop " + id + " skipped · a turn is running")
		return
	}
	s.notice("loop " + id)
	s.startTurn(prompt)
}

func (s *server) handler() http.Handler {
	mux := http.NewServeMux()
	static, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}
	file := http.FileServer(http.FS(static))
	mux.Handle("/static/", http.StripPrefix("/static/", cacheOff(file)))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFileFS(w, r, static, "index.html")
	})
	mux.HandleFunc("/events", s.events)
	mux.HandleFunc("/api/state", s.state)
	mux.HandleFunc("/api/prompt", s.prompt)
	mux.HandleFunc("/api/steer", s.steer)
	mux.HandleFunc("/api/cancel", s.doCancel)
	mux.HandleFunc("/api/permission", s.permission)
	mux.HandleFunc("/api/question", s.questionAPI)
	mux.HandleFunc("/api/command", s.command)
	mux.HandleFunc("/api/commands", s.commandsAPI)
	mux.HandleFunc("/api/inspect", s.inspectAPI)
	mux.HandleFunc("/api/sessions", s.sessionsAPI)
	mux.HandleFunc("/api/panel", s.panelAPI)
	mux.HandleFunc("/api/workspace", s.workspaceAPI)
	mux.HandleFunc("/api/agent", s.agentAPI)
	mux.HandleFunc("/api/job", s.jobAPI)
	mux.HandleFunc("/api/ptc", s.ptcAPI)
	mux.HandleFunc("/api/trajectory", s.trajectoryAPI)
	mux.HandleFunc("/api/change", s.changeAPI)
	mux.HandleFunc("/api/new", s.newChat)
	mux.HandleFunc("/api/load", s.loadSession)
	mux.HandleFunc("/api/upload", s.upload)
	mux.HandleFunc("/api/complete", s.completeAPI)
	return mux
}

func (s *server) hello() helloPayload {
	h := helloPayload{
		Cwd:      s.opts.Cwd,
		Provider: s.opts.Provider,
		Version:  s.opts.Version,
	}
	if s.opts.Agent != nil {
		modes := s.opts.Agent.Settings()
		h.Model = modes.Model
		h.Yolo = modes.AutoApprove
		h.Auto = modes.AutoMode
		h.Edits = modes.AutoEdits
		h.Plan = modes.PlanMode
		h.ReadOnly = modes.ReadOnly
		h.Mode = sessionMode(s.opts.Agent)
	}
	if s.opts.Session != nil {
		h.Session = s.opts.Session.Meta.ID
		h.Title = s.opts.Session.Meta.Title
	}
	return h
}

func (s *server) snapshot() (helloPayload, []*entry, *permPayload, []agent.Question, statusPayload) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ents := append([]*entry(nil), s.entries...)
	var p *permPayload
	if s.pending != nil {
		pp := s.permOf(s.pending.call)
		p = &pp
	}
	var qs []agent.Question
	if s.asking != nil {
		qs = s.asking.questions
	}
	return s.hello(), ents, p, qs, s.statusLocked()
}

func (s *server) state(w http.ResponseWriter, r *http.Request) {
	hello, ents, p, qs, st := s.snapshot()
	writeJSON(w, map[string]any{
		"hello":      hello,
		"entries":    ents,
		"permission": p,
		"questions":  qs,
		"status":     st,
	})
}

func (s *server) events(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "stream unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	ch := make(chan envelope, 64)
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	replay := append([]envelope(nil), s.log...)
	hello := envelope{Type: "hello", Hello: ptr(s.hello())}
	s.mu.Unlock()

	writeSSE(w, hello)
	fl.Flush()
	for _, e := range replay {
		writeSSE(w, e)
	}
	fl.Flush()

	ctx := r.Context()
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	defer func() {
		s.mu.Lock()
		delete(s.subs, ch)
		s.mu.Unlock()
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case e := <-ch:
			writeSSE(w, e)
			fl.Flush()
		case <-tick.C:
			s.mu.Lock()
			st := s.statusLocked()
			s.mu.Unlock()
			writeSSE(w, envelope{Type: "status", Status: &st})
			fl.Flush()
		}
	}
}

// completeAPI lists @path completions for the composer, the same source
// the TUI's tab completion uses.
func (s *server) completeAPI(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, prompt.CompletePath(s.opts.Cwd, r.URL.Query().Get("q")))
}

func ptr[T any](v T) *T { return &v }

// cacheOff forces revalidation of the embedded assets. The embed FS has no
// modtimes, so without this browsers heuristically cache app.js/app.css and
// keep showing a stale UI after an upgrade.
func cacheOff(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		h.ServeHTTP(w, r)
	})
}

func (s *server) emit(e envelope) {
	s.mu.Lock()
	s.seq++
	e.Seq = s.seq
	s.log = append(s.log, e)
	if len(s.log) > 2000 {
		s.log = s.log[len(s.log)-1500:]
	}
	for ch := range s.subs {
		select {
		case ch <- e:
		default:
		}
	}
	s.mu.Unlock()
}

func (s *server) prompt(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	text := strings.TrimSpace(body.Text)
	if text == "" {
		http.Error(w, "empty", http.StatusBadRequest)
		return
	}
	if err := s.submit(text); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, map[string]string{"ok": "1"})
}

func (s *server) submit(text string) error {
	if strings.HasPrefix(text, "/") {
		return s.runCommand(text)
	}
	if strings.HasPrefix(text, "!") {
		s.runShell(strings.TrimSpace(text[1:]))
		return nil
	}
	s.mu.Lock()
	if s.running {
		s.queued = append(s.queued, text)
		n := len(s.queued)
		s.mu.Unlock()
		s.emit(envelope{Type: "notice", Notice: fmt.Sprintf("queued %d · sends when this turn ends", n)})
		s.emitStatus()
		return nil
	}
	s.mu.Unlock()
	s.startTurn(text)
	return nil
}

func (s *server) startTurn(text string) {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.running = true
	s.phase = "thinking"
	s.started = time.Now()
	s.status = ""
	ctx, cancel := context.WithCancel(s.life)
	s.cancel = cancel
	s.mu.Unlock()

	s.upsert(&entry{Kind: "user", Text: text})
	s.add(&entry{Kind: "thinking"})
	s.emitStatus()
	if s.opts.Typed != nil {
		s.opts.Typed.Note(text)
	}
	blocks := []llm.Block{{Type: llm.BlockText, Text: text}}
	expanded, atts := prompt.ExpandMentions(s.opts.Cwd, text)
	if len(atts) > 0 {
		names := make([]string, len(atts))
		for i, a := range atts {
			names[i] = a.Path
		}
		s.upsert(&entry{Kind: "notice", Text: "attached " + strings.Join(names, " · ")})
		blocks = append([]llm.Block{{Type: llm.BlockText, Text: expanded}}, prompt.ImageBlocks(atts)...)
	}
	ag := s.opts.Agent
	go func() {
		err := ag.RunBlocks(ctx, blocks, s.apply)
		s.finish(err)
	}()
}

func (s *server) finish(err error) {
	s.mu.Lock()
	s.running = false
	s.phase = ""
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	queued := s.queued
	s.queued = nil
	elapsed := time.Since(s.started)
	s.mu.Unlock()
	switch {
	case err == nil:
		s.statusSet("done in " + textfmt.Duration(elapsed))
	case err == context.Canceled:
		s.upsert(&entry{Kind: "error", Text: "cancelled after " + textfmt.Duration(elapsed), Error: true})
		s.statusSet("cancelled")
	default:
		s.upsert(&entry{Kind: "error", Text: err.Error(), Error: true})
		s.statusSet("error")
	}
	s.emitStatus()
	if err == nil && len(queued) > 0 {
		s.startTurn(queued[0])
		s.mu.Lock()
		s.queued = queued[1:]
		s.mu.Unlock()
		return
	}
	// An active goal keeps the session going after the turn, the way
	// /goal works in the TUI, until the model marks it complete or
	// blocked (or the round cap ends it).
	if err == nil && s.opts.Agent != nil {
		if cont := s.opts.Agent.GoalContinuation(); cont != "" {
			s.notice("◉ goal round: " + cont)
			s.startTurn(cont)
		}
	}
}

func (s *server) steer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	text := strings.TrimSpace(body.Text)
	if text == "" {
		http.Error(w, "empty", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	running := s.running
	s.mu.Unlock()
	if !running {
		http.Error(w, "nothing is running", http.StatusConflict)
		return
	}
	s.opts.Agent.Steer(text)
	s.emit(envelope{Type: "notice", Notice: "steering: " + textfmt.OneLine(text)})
	writeJSON(w, map[string]string{"ok": "1"})
}

func (s *server) command(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if err := s.runCommand(strings.TrimSpace(body.Text)); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, map[string]string{"ok": "1"})
}

func (s *server) runShell(cmdline string) {
	if cmdline == "" {
		s.upsert(&entry{Kind: "error", Text: "usage: !<command>", Error: true})
		return
	}
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		s.upsert(&entry{Kind: "error", Text: "wait for the current turn to finish before running a command", Error: true})
		return
	}
	s.running = true
	s.phase = "running your command"
	s.started = time.Now()
	ctx, cancel := context.WithCancel(s.life)
	s.cancel = cancel
	s.mu.Unlock()
	input, _ := json.Marshal(map[string]string{"command": cmdline})
	call := agent.ToolCall{ID: fmt.Sprintf("shell-%d", time.Now().UnixNano()), Name: "shell", Input: input}
	s.apply(agent.Event{Kind: agent.EvToolRun, Call: &call})
	s.emitStatus()
	cwd := s.opts.Cwd
	go func() {
		start := time.Now()
		out, err := tool.Bash{Root: cwd}.Run(ctx, input)
		isErr := err != nil
		if isErr {
			out = "Error: " + err.Error()
		}
		if out == "" {
			out = "(no output)"
		}
		s.apply(agent.Event{Kind: agent.EvToolResult, Call: &call, Output: out, IsError: isErr, Duration: time.Since(start)})
		s.opts.Agent.AddContext("I ran `" + cmdline + "`:\n" + out)
		s.finish(nil)
	}()
}

func (s *server) statusLocked() statusPayload {
	st := statusPayload{
		Running: s.running,
		Phase:   flavor.ForPhase(s.phase, time.Now(), s.started),
		Queued:  len(s.queued),
		Status:  s.status,
	}
	if s.opts.Agent != nil {
		modes := s.opts.Agent.Settings()
		st.Yolo = modes.AutoApprove
		st.Auto = modes.AutoMode
		st.Edits = modes.AutoEdits
		st.Plan = modes.PlanMode
		st.ReadOnly = modes.ReadOnly
		st.Mode = sessionMode(s.opts.Agent)
		st.Verbose = s.verbose
		if usd, ok := s.opts.Agent.UsageSnapshot().Cost(s.opts.Agent.Settings().Price); ok {
			st.Cost = llm.FormatUSD(usd)
		}
		est := s.opts.Agent.TokenEstimate()
		if !s.running {
			est = s.opts.Agent.EstimateTokens()
		}
		if lim := s.opts.Agent.CompactThreshold(); lim > 0 {
			st.CtxPct = est * 100 / lim
			if st.CtxPct > 100 {
				st.CtxPct = 100
			}
		}
	}
	if s.running && !s.started.IsZero() {
		st.Elapsed = textfmt.Duration(time.Since(s.started))
	}
	return st
}

func (s *server) emitStatus() {
	s.mu.Lock()
	st := s.statusLocked()
	s.mu.Unlock()
	s.emit(envelope{Type: "status", Status: &st})
}

func (s *server) statusSet(v string) {
	s.mu.Lock()
	s.status = v
	s.mu.Unlock()
}

func writeSSE(w http.ResponseWriter, e envelope) {
	if e.Seq > 0 {
		fmt.Fprintf(w, "id: %d\n", e.Seq)
	}
	b, _ := json.Marshal(e)
	fmt.Fprintf(w, "data: %s\n\n", b)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func summarizeInput(name, raw string, max int, cwd string) string {
	var m map[string]any
	if json.Unmarshal([]byte(raw), &m) != nil {
		return textfmt.Clip(raw, max)
	}
	for _, k := range []string{"command", "pattern", "path", "url", "description"} {
		if v, ok := m[k].(string); ok {
			if k == "path" {
				v = textfmt.RelTo(cwd, v)
			}
			return textfmt.Clip(v, max)
		}
	}
	return textfmt.Clip(raw, max)
}
