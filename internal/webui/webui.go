// Package webui serves the session in a browser: the same agent as the TUI,
// streamed over SSE, with permission prompts answered from the page.
package webui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"syscall"
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
	"github.com/xmonader/janetcode/internal/skill"
	"github.com/xmonader/janetcode/internal/textfmt"
	"github.com/xmonader/janetcode/internal/tool"
	"github.com/xmonader/janetcode/internal/weburl"
)

// Options configures the browser UI.
type Options struct {
	Agent *agent.Agent
	// Skills says which skills this session has attached and what they
	// cost, for /skills to price them.
	Skills   *skill.Injector
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
	// CD is the session's working directory and everything rooted at it;
	// /cd moves them together. Nil disables /cd.
	CD *agent.CD
	// Workspace is the shared host file tools use; /ssh and /docker retarget it.
	Workspace *tool.Workspace

	// Typed collects hosts the user names in their own messages so
	// fetch_url may reach them; @path attachments are expanded with it.
	Typed      *weburl.Set
	Config     *config.Config
	ConfigPath string
	// Models switches the session onto another model's provider.
	Models func(string) (llm.Provider, string, error)
	// BuildProvider constructs the client for a configured provider by
	// name, so the model switcher can ask each provider what it serves.
	// Nil leaves the switcher with the config's static lists.
	BuildProvider func(name string) (llm.Provider, error)
	// MCP describes the MCP servers, for /mcp.
	MCP *MCPOptions
	// Doctor is the same checkup janetcode --doctor prints. It makes a
	// live model call, so it takes the server's context and stops when
	// the server does.
	Doctor func(context.Context, io.Writer) error
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

// Run listens on opts.Addr and serves until ctx ends. When the asked-for
// port is already taken — a second --web on the same box — it serves on a
// random port instead of refusing to start, and says so next to the URL.
func Run(ctx context.Context, opts Options) error {
	addr := ListenAddr(opts.Addr)
	ln, err := net.Listen("tcp", addr)
	if err != nil && errors.Is(err, syscall.EADDRINUSE) {
		host, _, splitErr := net.SplitHostPort(addr)
		if splitErr == nil {
			ln, err = net.Listen("tcp", net.JoinHostPort(host, "0"))
			if err == nil {
				fmt.Fprintf(os.Stderr, "janetcode: %s is busy; serving on %s instead\n", addr, displayAddr(ln.Addr()))
			}
		}
	}
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
	served := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
		case <-served:
			// Serve returned on its own; nothing to shut down, and
			// waiting on ctx here would park this goroutine for the rest
			// of the process.
			return
		}
		shctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := hs.Shutdown(shctx); err != nil {
			// An open /events stream never goes idle, so Shutdown waits
			// out its whole grace period and then leaves those
			// connections — and the handler goroutine behind each one —
			// running. Close hangs up on them.
			_ = hs.Close()
		}
	}()
	err = hs.Serve(ln)
	close(served)
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
	// openedAt is when this server came up, which is what /perf reports
	// the in-turn time as a share of: the rest of it is the user
	// reading and typing, and that is not the harness being slow.
	openedAt time.Time
	// The model switcher's live listing: what the providers served last,
	// when they served it, and whether a sweep is running.
	modelsMu       sync.Mutex
	modelsAt       time.Time
	modelsCache    []modelGroup
	modelsFetching bool
	// lastAssistant is the entry the current turn's answer streams into,
	// stamped with the turn's bill when the turn finishes.
	lastAssistant string

	// dirMu guards dir, the directory this session is rooted at. It is
	// separate from mu because the hello payload reads it while mu is
	// held.
	dirMu sync.RWMutex
	dir   string

	// sessMu guards opts.Session, the one option /new, /load and /fork
	// replace while other requests are reading it. Everything reads it
	// through sess().
	sessMu sync.RWMutex
}

// sess is the session this server is logging to, or nil.
func (s *server) sess() *session.Session {
	s.sessMu.RLock()
	defer s.sessMu.RUnlock()
	return s.opts.Session
}

// setSession points the server at another session (/new, /load, /fork).
func (s *server) setSession(v *session.Session) {
	s.sessMu.Lock()
	s.opts.Session = v
	s.sessMu.Unlock()
}

// cwd is the directory this session is rooted at.
func (s *server) cwd() string {
	s.dirMu.RLock()
	defer s.dirMu.RUnlock()
	return s.dir
}

// setCwd records where /cd moved the session.
func (s *server) setCwd(dir string) {
	s.dirMu.Lock()
	defer s.dirMu.Unlock()
	s.dir = dir
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
	// Effort is the reasoning tier the session asks for, empty when it
	// is the provider's own default — the chip beside the mode.
	Effort  string `json:"effort,omitempty"`
	Verbose bool   `json:"verbose"`
	Status  string `json:"status,omitempty"`
	Cost    string `json:"cost,omitempty"`
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
		opts:     opts,
		life:     life,
		dir:      opts.Cwd,
		subs:     map[chan envelope]struct{}{},
		openedAt: time.Now(),
	}
	// A server pointed at a session with history starts with that
	// history on the thread — the way /resume rebuilds it — instead of a
	// blank transcript until something new happens.
	if opts.Session != nil {
		if msgs := session.FoldSurface(opts.Session.Events()); len(msgs) > 0 {
			s.replayHistory(msgs)
		}
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
	mux.HandleFunc("/api/models", s.modelsAPI)
	mux.HandleFunc("/api/inspect", s.inspectAPI)
	mux.HandleFunc("/api/sessions", s.sessionsAPI)
	mux.HandleFunc("/api/panel", s.panelAPI)
	mux.HandleFunc("/api/workspace", s.workspaceAPI)
	mux.HandleFunc("/api/agent", s.agentAPI)
	mux.HandleFunc("/api/job", s.jobAPI)
	mux.HandleFunc("/api/ptc", s.ptcAPI)
	mux.HandleFunc("/api/trajectory", s.trajectoryAPI)
	mux.HandleFunc("/api/trajectory/timeline", s.trajectoryTimelineAPI)
	mux.HandleFunc("/api/trajectory/raw", s.trajectoryRawAPI)
	mux.HandleFunc("/api/term", s.termAPI)
	mux.HandleFunc("/api/change", s.changeAPI)
	mux.HandleFunc("/api/new", s.newChat)
	mux.HandleFunc("/api/load", s.loadSession)
	mux.HandleFunc("/api/upload", s.upload)
	mux.HandleFunc("/api/complete", s.completeAPI)
	mux.HandleFunc("/api/mermaid", s.mermaidAPI)
	return mux
}

func (s *server) hello() helloPayload {
	h := helloPayload{
		Cwd:      s.cwd(),
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
	if s.sess() != nil {
		h.Session = s.sess().Meta.ID
		h.Title = s.sess().Meta.Title
	}
	return h
}

func (s *server) snapshot() (helloPayload, []entry, *permPayload, []agent.Question, statusPayload) {
	s.mu.Lock()
	// Copies, not the pointers: /api/state used to serialise entries the
	// agent's goroutine was still writing to.
	ents := make([]entry, 0, len(s.entries))
	for _, e := range s.entries {
		ents = append(ents, *e)
	}
	var p *permPayload
	if s.pending != nil {
		pp := s.permOf(s.pending.call)
		p = &pp
	}
	var qs []agent.Question
	if s.asking != nil {
		qs = s.asking.questions
	}
	f := s.stateLocked()
	s.mu.Unlock()
	return s.hello(), ents, p, qs, s.statusOf(f)
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
		case e, ok := <-ch:
			if !ok {
				return // fell behind; the browser reconnects and replays
			}
			writeSSE(w, e)
			fl.Flush()
		case <-tick.C:
			st := s.statusNow()
			writeSSE(w, envelope{Type: "status", Status: &st})
			fl.Flush()
		}
	}
}

// completeAPI lists @path completions for the composer, the same source
// the TUI's tab completion uses.
func (s *server) completeAPI(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, prompt.CompletePath(s.cwd(), r.URL.Query().Get("q")))
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
			// This page is not keeping up. Dropping the event on the
			// floor left it quietly out of step with the session for as
			// long as it stayed open; ending its stream instead makes the
			// browser reconnect, and a new stream replays the log.
			delete(s.subs, ch)
			close(ch)
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

// begin marks the session busy and returns the context the work runs on.
// ok is false when something is already running: a turn, a shell command
// and /compact each own the agent's history while they run, so only one
// of them may. esc cancels the context; end releases it.
func (s *server) begin(phase string) (context.Context, bool) {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return nil, false
	}
	s.running = true
	s.phase = phase
	s.started = time.Now()
	ctx, cancel := context.WithCancel(s.life)
	s.cancel = cancel
	s.mu.Unlock()
	return ctx, true
}

// end marks the session idle and hands back the next queued prompt —
// taken under the lock, so one arriving in the same moment is not lost —
// and how long the work took. A failed turn drops the queue: what was
// queued was queued behind work that did not happen.
func (s *server) end(failed bool) (next string, elapsed time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	elapsed = s.idleLocked()
	switch {
	case failed:
		s.queued = nil
	case len(s.queued) > 0:
		next, s.queued = s.queued[0], s.queued[1:]
	}
	return next, elapsed
}

// idle marks the session idle and leaves the queue alone, for work that
// starts a turn of its own afterwards (a recipe's handover).
func (s *server) idle() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.idleLocked()
}

func (s *server) idleLocked() time.Duration {
	s.running = false
	s.phase = ""
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	return time.Since(s.started)
}

func (s *server) startTurn(text string) {
	ctx, ok := s.begin("thinking")
	if !ok {
		return
	}
	s.statusSet("")

	s.add(entry{Kind: "user", Text: text})
	s.add(entry{Kind: "thinking"})
	s.emitStatus()
	if s.opts.Typed != nil {
		s.opts.Typed.Note(text)
	}
	blocks := []llm.Block{{Type: llm.BlockText, Text: text}}
	expanded, atts := prompt.ExpandMentions(s.cwd(), text)
	if len(atts) > 0 {
		names := make([]string, len(atts))
		for i, a := range atts {
			names[i] = a.Path
		}
		s.add(entry{Kind: "notice", Text: "attached " + strings.Join(names, " · ")})
		blocks = append([]llm.Block{{Type: llm.BlockText, Text: expanded}}, prompt.ImageBlocks(atts)...)
	}
	ag := s.opts.Agent
	go func() {
		err := ag.RunBlocks(ctx, blocks, s.apply)
		s.finish(err)
	}()
}

func (s *server) finish(err error) {
	next, elapsed := s.end(err != nil)
	s.touchRuns()
	switch {
	case err == nil:
		s.statusSet("done in " + textfmt.Duration(elapsed))
	case err == context.Canceled:
		s.add(entry{Kind: "error", Text: "cancelled after " + textfmt.Duration(elapsed), Error: true})
		s.statusSet("cancelled")
	default:
		s.add(entry{Kind: "error", Text: err.Error(), Error: true})
		s.statusSet("error")
	}
	s.emitStatus()
	if next != "" {
		s.startTurn(next)
		return
	}
	// An active goal keeps the session going after the turn, the way
	// /goal works in the TUI, until the model marks it complete or
	// blocked (or the round cap ends it).
	if err == nil && s.opts.Agent != nil {
		if cont := s.opts.Agent.GoalContinuation(s.life); cont != "" {
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
		s.add(entry{Kind: "error", Text: "usage: !<command>", Error: true})
		return
	}
	ctx, ok := s.begin("running your command")
	if !ok {
		s.add(entry{Kind: "error", Text: "wait for the current turn to finish before running a command", Error: true})
		return
	}
	input, _ := json.Marshal(map[string]string{"command": cmdline})
	call := agent.ToolCall{ID: fmt.Sprintf("shell-%d", time.Now().UnixNano()), Name: "shell", Input: input}
	s.apply(agent.Event{Kind: agent.EvToolRun, Call: &call})
	s.emitStatus()
	cwd := s.cwd()
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

// sessionState is what the status line takes from the server itself. It
// is copied out under mu so the rest of the status — which asks the agent
// for its settings, its usage and an estimate over the whole history — is
// computed without holding the lock the turn needs. Every open page asks
// for this twice a second.
type sessionState struct {
	running bool
	phase   string
	queued  int
	status  string
	verbose bool
	started time.Time
}

func (s *server) stateLocked() sessionState {
	return sessionState{
		running: s.running, phase: s.phase, queued: len(s.queued),
		status: s.status, verbose: s.verbose, started: s.started,
	}
}

func (s *server) statusNow() statusPayload {
	s.mu.Lock()
	f := s.stateLocked()
	s.mu.Unlock()
	return s.statusOf(f)
}

func (s *server) statusOf(f sessionState) statusPayload {
	st := statusPayload{
		Running: f.running,
		Phase:   flavor.ForPhase(f.phase, time.Now(), f.started),
		Queued:  f.queued,
		Status:  f.status,
		Verbose: f.verbose,
	}
	if s.opts.Agent != nil {
		modes := s.opts.Agent.Settings()
		st.Yolo = modes.AutoApprove
		st.Auto = modes.AutoMode
		st.Edits = modes.AutoEdits
		st.Plan = modes.PlanMode
		st.ReadOnly = modes.ReadOnly
		st.Mode = sessionMode(s.opts.Agent)
		st.Effort = modes.Effort
		if usd, ok := s.opts.Agent.UsageSnapshot().Cost(modes.Price); ok {
			st.Cost = llm.FormatUSD(usd)
		}
		est := s.opts.Agent.TokenEstimate()
		if !f.running {
			est = s.opts.Agent.EstimateTokens()
		}
		if lim := s.opts.Agent.CompactThreshold(); lim > 0 {
			st.CtxPct = est * 100 / lim
			if st.CtxPct > 100 {
				st.CtxPct = 100
			}
		}
	}
	if f.running && !f.started.IsZero() {
		st.Elapsed = textfmt.Duration(time.Since(f.started))
	}
	return st
}

func (s *server) emitStatus() {
	st := s.statusNow()
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
	// MCP tools are third-party code, and their argument values often
	// carry a credential the model copied out of context — an api_key, a
	// bearer token. None of the keys below are theirs, so this line fell
	// through to printing the whole raw JSON: the summary of an MCP call
	// was its arguments, verbatim, in the browser and in the event stream
	// behind it. Name the keys instead, as the terminal front-end does.
	if strings.HasPrefix(name, "mcp__") {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return textfmt.Clip("{"+strings.Join(keys, ", ")+"}", max)
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
