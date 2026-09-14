package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/textfmt"
	"github.com/xmonader/janetcode/internal/tool"
)

// pinCommand adds a pin or lists them.
func (m *model) pinCommand(arg string) {
	ag := m.opts.Agent
	if arg == "" {
		pins := ag.PinsSnapshot()
		if len(pins) == 0 {
			m.transcript.info("no pins: /pin <note> or /pin @path keeps something in the system prompt for the whole session")
			return
		}
		var sb strings.Builder
		for i, p := range pins {
			fmt.Fprintf(&sb, "%d. %s\n", i+1, textfmt.Clip(strings.ReplaceAll(p, "\n", " "), 120))
		}
		sb.WriteString("/unpin <n> removes one")
		m.transcript.info(strings.TrimRight(sb.String(), "\n"))
		return
	}
	text, err := agent.PinText(m.opts.Cwd, arg)
	if err != nil {
		m.transcript.errorf("pin: %v", err)
		return
	}
	arg = text
	n := ag.Pin(arg)
	m.savePins()
	m.note(fmt.Sprintf("pinned (%d): %s", n, textfmt.Clip(strings.ReplaceAll(arg, "\n", " "), 100)))
}

func (m *model) unpinCommand(args []string) {
	if len(args) != 1 {
		m.transcript.errorf("usage: /unpin <n>")
		return
	}
	n, err := strconv.Atoi(args[0])
	if err != nil || !m.opts.Agent.Unpin(n) {
		m.transcript.errorf("no pin %s; /pin lists them", args[0])
		return
	}
	m.savePins()
	m.note(fmt.Sprintf("unpinned %d", n))
}

func (m *model) savePins() {
	if m.opts.Session == nil {
		return
	}
	if err := m.opts.Session.SavePins(m.opts.Agent.PinsSnapshot()); err != nil {
		m.transcript.errorf("could not save pins: %v", err)
	}
}

// titleMsg carries a generated session title.
type titleMsg struct {
	gen   int
	title string
}

// autoTitle asks the model for a short title in the background; on any
// failure the first user message provides one.
func (m *model) autoTitle() tea.Cmd {
	m.titleGen++
	gen := m.titleGen
	ag := m.opts.Agent
	msgs := append([]llm.Message(nil), ag.Messages...)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		title, err := session.GenerateTitle(ctx, ag.Provider, ag.Settings().Model, msgs)
		if err != nil || title == "" {
			title = session.AutoTitle(msgs)
		}
		return titleMsg{gen: gen, title: title}
	}
}

// forkSession copies the conversation. Plain /fork switches into the copy;
// /fork stay keeps this session live and lists the copy for /resume.
func (m *model) forkSession(arg string) {
	if m.opts.Session == nil {
		m.transcript.errorf("no session to fork")
		return
	}
	if m.running {
		m.transcript.errorf("wait for the current turn to finish before forking")
		return
	}
	stay, title := session.StayArg(arg)
	ns, err := session.Fork(dirOf(m.opts.Session), m.opts.Session, title)
	if err != nil {
		m.transcript.errorf("fork: %v", err)
		return
	}
	if stay {
		m.note(fmt.Sprintf("forked → %s (%s) · this session stays here, /resume the copy", ns.Meta.ID, ns.Meta.Title))
		return
	}
	old := m.opts.Session.Label()
	m.opts.Session = ns
	if err := m.opts.Agent.ForkSession(ns); err != nil {
		m.transcript.errorf("fork: %v", err)
	}
	m.note(fmt.Sprintf("forked %s → %s (%s) · this session continues here · /resume lists both", old, ns.Meta.ID, ns.Meta.Title))
}

func (m *model) btwCommand(q string) tea.Cmd {
	q = strings.TrimSpace(q)
	if q == "" {
		m.transcript.info("usage: /btw <question>")
		m.refresh(false)
		return nil
	}
	ag := m.opts.Agent
	if ag == nil {
		m.transcript.errorf("no agent")
		m.refresh(false)
		return nil
	}
	send := m.send
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		a, err := ag.AskOnce(ctx, q)
		send(btwMsg{q: q, a: a, err: err})
	}()
	m.note("btw…")
	m.refresh(false)
	return nil
}

// setTitle names the session.
func (m *model) setTitle(title string) {
	if m.opts.Session == nil {
		m.transcript.errorf("no session")
		return
	}
	if title == "" {
		m.note("title: " + m.opts.Session.Label() + " · /title <text> renames")
		return
	}
	m.titleGen++ // a pending generated title must not overwrite this
	if err := m.opts.Session.SetTitle(title); err != nil {
		m.transcript.errorf("save title: %v", err)
		return
	}
	m.note("titled: " + title)
}

// searchSessions finds earlier conversations mentioning the words.
func (m *model) searchSessions(query string) {
	if query == "" {
		m.transcript.errorf("usage: /history <words>")
		return
	}
	hits, err := session.Search(dirOf(m.opts.Session), "", query, 15)
	if err != nil {
		m.transcript.errorf("search: %v", err)
		return
	}
	if len(hits) == 0 {
		m.transcript.info("no session mentions " + query)
		return
	}
	var sb strings.Builder
	for _, h := range hits {
		where := ""
		if h.Info.Meta.Cwd != m.opts.Cwd {
			where = " in " + shortPath(h.Info.Meta.Cwd)
		}
		fmt.Fprintf(&sb, "  %s  %s%s\n      %s: %s\n", h.Info.Meta.ID, h.Info.Label(), where, h.Role, h.Snippet)
	}
	sb.WriteString("/resume <id> reopens one (sessions from other directories open there when you cd)")
	m.transcript.info(sb.String())
}

type btwMsg struct {
	q, a string
	err  error
}

// find scrolls the transcript to the next line containing q (case-insensitive),
// starting below the current top line and wrapping around.
// export writes the conversation as Markdown, to path or to a file named
// after the session in the working directory.
func (m *model) export(path string) {
	if path == "" {
		path = "janetcode-" + m.opts.Session.Meta.ID + ".md"
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(m.opts.Cwd, path)
	}
	md := session.Markdown(m.opts.Session.Meta, m.opts.Agent.Messages)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		m.transcript.errorf("export: %v", err)
		return
	}
	if err := tool.AtomicWrite(path, []byte(md)); err != nil {
		m.transcript.errorf("export: %v", err)
		return
	}
	m.note(fmt.Sprintf("exported %d messages to %s", len(m.opts.Agent.Messages), shortPath(path)))
}

// resume swaps the conversation for a saved one.
func (m *model) resume(arg string) {
	dir := dirOf(m.opts.Session)
	id := arg
	if n, err := strconv.Atoi(arg); err == nil {
		infos, err := session.List(dir, m.opts.Cwd)
		if err != nil {
			m.transcript.errorf("list sessions: %v", err)
			return
		}
		k := 0
		id = ""
		for _, in := range infos {
			if in.Meta.ID == m.opts.Session.Meta.ID || in.Messages == 0 {
				continue
			}
			if k++; k == n {
				id = in.Meta.ID
				break
			}
		}
		if id == "" {
			m.transcript.errorf("no session numbered %d (see /resume)", n)
			return
		}
	}
	sess, history, err := session.Open(dir, id)
	if err != nil {
		m.transcript.errorf("resume %s: %v", id, err)
		return
	}
	m.opts.Session = sess
	m.opts.Agent.Usage = llm.Usage{}
	if err := m.opts.Agent.OpenSession(sess, history); err != nil {
		m.transcript.errorf("%v", err)
	}
	m.titleGen++ // a title generated for the session we just left must not land here
	m.transcript = newTranscriptStyled(m.transcript.styleName)
	m.transcript.banner(m.welcome(resumedLine(sess.Meta.ID, len(history))))
	m.transcript.loadHistory(history)
	m.lastTurn = ""
	m.lastVerdict = ""
	m.repin()
}

func dirOf(s *session.Session) string {
	return filepath.Dir(s.Path())
}
