// The agent dashboard: every session of this directory grouped by what it
// needs from you. One janetcode web process drives one live session, so
// the dashboard is the live session plus the background runs (--bg and
// the dashboard's own dispatch box) and the stored sessions at rest. Only
// the live session and background runs can be replied to or stopped; a
// stored session is opened (/api/load) to continue it.
package webui

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// Dashboard groups.
const (
	groupAwaiting = "awaiting"
	groupWorking  = "working"
	groupIdle     = "idle"
)

// maxIdleRows keeps the idle group to the recent sessions.
const maxIdleRows = 30

type dashRow struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Group   string `json:"group"`
	Kind    string `json:"kind"` // live, bg or stored
	Last    string `json:"last,omitempty"`
	Waiting string `json:"waiting,omitempty"` // what an awaiting row waits for
	Updated string `json:"updated"`
	Pinned  bool   `json:"pinned"`
	Current bool   `json:"current"`
	Replies bool   `json:"canReply"`
	Stops   bool   `json:"canStop"`
	// Pending is the question a background run keeps for you (it sorts the
	// row into "Needs you"), even when the run has finished.
	Pending *dashPending `json:"pending,omitempty"`
}

type dashPending struct {
	Text    string   `json:"text"`
	Choices []string `json:"choices,omitempty"`
	About   string   `json:"about,omitempty"`
	Asked   string   `json:"asked"`
	Done    bool     `json:"done,omitempty"` // the run ended: nothing to reply to
}

// dashboardRows classifies infos. live is the id of this process's
// session, whose state comes from the server rather than a sidecar.
func (s *server) dashboardRows(dir string, infos []session.Info, live string, running bool, waiting string) []dashRow {
	var rows []dashRow
	idle := 0
	for _, in := range infos {
		if in.Meta.Archived {
			continue
		}
		r := dashRow{
			ID: in.Meta.ID, Title: textfmt.Clip(in.Label(), 80), Kind: "stored", Group: groupIdle,
			Last: in.Last, Updated: in.Updated.Format(time.RFC3339), Pinned: in.Meta.Starred,
		}
		switch {
		case in.Meta.ID == live:
			r.Kind, r.Current = "live", true
			switch {
			case waiting != "":
				r.Group, r.Waiting, r.Replies, r.Stops = groupAwaiting, waiting, true, true
			case running:
				r.Group, r.Replies, r.Stops = groupWorking, true, true
			default:
				r.Replies = true
			}
		default:
			if path, err := session.BgSessionPath(dir, in.Meta.ID); err == nil {
				if st, ok, _ := session.ReadBgStatus(path); ok {
					r.Kind = "bg"
					switch st.State {
					case session.BgWaiting:
						r.Group, r.Replies, r.Stops = groupAwaiting, true, true
						r.Waiting = bgWaitingFor(st)
					case session.BgRunning:
						r.Group, r.Stops = groupWorking, true
						if st.Question != nil {
							r.Group, r.Replies, r.Waiting = groupAwaiting, true, bgWaitingFor(st)
						}
					}
					if p := st.Pending; p != nil {
						// Whatever its state, a run holding a question is
						// one that needs you; a finished one can only be
						// opened to continue.
						done := st.State != session.BgRunning && st.State != session.BgWaiting
						r.Group = groupAwaiting
						if r.Waiting == "" {
							r.Waiting = textfmt.Clip(p.Text, 160)
						}
						r.Pending = &dashPending{Text: textfmt.Clip(p.Text, 400), Choices: p.Choices, About: p.About, Asked: p.Asked.Format(time.RFC3339), Done: done}
					}
				}
			}
		}
		if r.Group == groupIdle {
			if idle++; idle > maxIdleRows && !r.Pinned {
				continue
			}
		}
		rows = append(rows, r)
	}
	return rows
}

func bgWaitingFor(st session.BgStatus) string {
	switch {
	case st.Question != nil && len(st.Question.Items) > 0:
		return textfmt.Clip(st.Question.Items[0].Prompt, 160)
	case st.Ask != nil:
		return textfmt.Clip(st.Ask.Tool+" "+st.Ask.Summary, 160)
	}
	return "an answer"
}

// liveWaiting says what the live session waits on you for, "" when
// nothing: an open permission prompt or question.
func (s *server) liveWaiting() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.pending != nil:
		return "permission to run " + textfmt.Clip(s.pending.call.Name, 80)
	case s.asking != nil && len(s.asking.questions) > 0:
		return textfmt.Clip(s.asking.questions[0].Prompt, 160)
	}
	return ""
}

func (s *server) dashboardAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
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
	live := ""
	if cur := s.sess(); cur != nil {
		live = cur.Meta.ID
	}
	rows := s.dashboardRows(dir, infos, live, s.isRunning(), s.liveWaiting())
	groups := map[string][]dashRow{groupAwaiting: {}, groupWorking: {}, groupIdle: {}}
	for _, row := range rows {
		groups[row.Group] = append(groups[row.Group], row)
	}
	writeJSON(w, map[string]any{"groups": groups, "dispatch": s.opts.Dispatch != nil})
}

// dashBody is every field the /api/dashboard/* actions read.
type dashBody struct {
	ID     string `json:"id"`
	Text   string `json:"text"`
	Prompt string `json:"prompt"`
}

func (s *server) dashboardOp(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var b dashBody
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&b); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	var code int
	var err error
	var out map[string]string
	switch strings.TrimPrefix(r.URL.Path, "/api/dashboard/") {
	case "reply":
		code, err = s.dashReply(b)
	case "stop":
		code, err = s.dashStop(b)
	case "dispatch":
		code, out, err = s.dashDispatch(r, b)
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), code)
		return
	}
	if out == nil {
		out = map[string]string{}
	}
	out["ok"] = "1"
	writeJSON(w, out)
}

func (s *server) isLive(id string) bool {
	cur := s.sess()
	return cur != nil && cur.Meta.ID == id
}

type dashErr string

func (e dashErr) Error() string { return string(e) }

func (s *server) dashReply(b dashBody) (int, error) {
	text := strings.TrimSpace(b.Text)
	if text == "" {
		return http.StatusBadRequest, dashErr("a reply is required")
	}
	if s.isLive(b.ID) {
		s.mu.Lock()
		asking, running := s.asking, s.running
		s.mu.Unlock()
		switch {
		case s.liveWaiting() != "" && asking == nil:
			return http.StatusConflict, dashErr("this session waits for a permission decision: open it to allow or deny")
		case asking != nil:
			// A typed reply answers every open question as free text.
			s.mu.Lock()
			req := s.asking
			s.asking = nil
			s.mu.Unlock()
			if req == nil {
				return http.StatusConflict, dashErr("no question pending")
			}
			ans := make([]agent.Answer, len(req.questions))
			for i := range ans {
				ans[i] = agent.Answer{Label: agent.OtherLabel, Reason: text}
			}
			s.emit(envelope{Type: "question"})
			select {
			case req.reply <- ans:
			default:
			}
			s.emitStatus()
			return http.StatusOK, nil
		case running:
			s.opts.Agent.Steer(text)
			s.emit(envelope{Type: "notice", Notice: "steering: " + textfmt.OneLine(text)})
			return http.StatusOK, nil
		}
		if err := s.submit(text); err != nil {
			return http.StatusConflict, err
		}
		return http.StatusOK, nil
	}
	path, st, ok, err := s.bgStatus(b.ID)
	if err != nil {
		return http.StatusNotFound, err
	}
	switch {
	case !ok:
		return http.StatusConflict, dashErr("this session is not running: open it from the sidebar to continue it")
	case st.Question != nil:
		answers := make([]string, len(st.Question.Items))
		for i := range answers {
			answers[i] = text
		}
		if err := session.WriteBgReply(path, session.BgReply{ID: st.Question.ID, Answers: answers}); err != nil {
			return http.StatusInternalServerError, err
		}
	case st.State == session.BgWaiting && st.Ask != nil:
		d := strings.ToLower(text)
		valid := false
		for _, v := range session.BgDecisions {
			valid = valid || v == d
		}
		if !valid {
			return http.StatusBadRequest, dashErr("this run waits for a permission decision: reply allow, session or deny")
		}
		if err := session.WriteBgAnswer(path, session.BgAnswer{ID: st.Ask.ID, Decision: d}); err != nil {
			return http.StatusInternalServerError, err
		}
	default:
		return http.StatusConflict, dashErr("this run is not waiting for an answer")
	}
	return http.StatusOK, nil
}

// bgStatus is the background status of session id; ok is false when the
// session is not a run that is still going.
func (s *server) bgStatus(id string) (path string, st session.BgStatus, ok bool, err error) {
	dir, err := session.Dir()
	if err != nil {
		return "", st, false, err
	}
	path, err = session.BgSessionPath(dir, id)
	if err != nil {
		return "", st, false, dashErr("unknown session")
	}
	if _, serr := os.Stat(path); serr != nil {
		return "", st, false, dashErr("unknown session")
	}
	st, has, _ := session.ReadBgStatus(path)
	return path, st, has && (st.State == session.BgRunning || st.State == session.BgWaiting), nil
}

func (s *server) dashStop(b dashBody) (int, error) {
	if s.isLive(b.ID) {
		s.mu.Lock()
		running := s.running
		if s.cancel != nil {
			s.cancel()
		}
		s.mu.Unlock()
		if !running {
			return http.StatusConflict, dashErr("nothing is running")
		}
		return http.StatusOK, nil
	}
	path, _, ok, err := s.bgStatus(b.ID)
	switch {
	case err != nil:
		return http.StatusNotFound, err
	case !ok:
		return http.StatusConflict, dashErr("this session is not running")
	}
	if err := session.StopBg(path); err != nil {
		return http.StatusConflict, err
	}
	return http.StatusOK, nil
}

func (s *server) dashDispatch(r *http.Request, b dashBody) (int, map[string]string, error) {
	prompt := strings.TrimSpace(b.Prompt)
	switch {
	case prompt == "":
		return http.StatusBadRequest, nil, dashErr("a prompt is required")
	case s.opts.Dispatch == nil:
		return http.StatusNotImplemented, nil, dashErr("starting a background run is not available here")
	}
	id, err := s.opts.Dispatch(r.Context(), prompt)
	if err != nil {
		return http.StatusInternalServerError, nil, err
	}
	return http.StatusOK, map[string]string{"id": id}, nil
}
