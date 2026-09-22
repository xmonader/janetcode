package webui

import (
	"strings"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/shell"
	"github.com/xmonader/janetcode/internal/tool"
)

func (s *server) addDir(arg string) {
	ws := s.opts.Workspace
	if strings.TrimSpace(arg) == "" {
		if ws == nil {
			s.errf("this session has no workspace to add a directory to")
			return
		}
		dirs := ws.ExtraDirs()
		if len(dirs) == 0 {
			s.notice("no extra directories · /add-dir <path> grants one")
			return
		}
		s.sheet("extra dirs", strings.Join(dirs, "\n"))
		return
	}
	if err := agent.AddDir(ws, s.sess(), arg); err != nil {
		s.errf("%v", err)
		return
	}
	s.notice("file tools can use " + arg)
}

// cd moves the session's working directory, the web form of the same
// move the terminal does. The browser keeps its thread: nothing about the
// conversation is reset.
func (s *server) cd(arg string) {
	cd := s.opts.CD
	if cd == nil {
		s.errf("this session has no working directory to change")
		return
	}
	if a := strings.TrimSpace(arg); a == "" || a == "status" {
		s.sheet("cd", cd.Where())
		return
	}
	status, err := cd.Change(arg)
	if err != nil {
		s.errf("%v", err)
		return
	}
	s.setCwd(cd.Dir())
	s.notice(status)
	s.emit(envelope{Type: "hello", Hello: ptr(s.hello())})
	s.emitStatus()
}

func (s *server) sandbox(arg string) {
	var sb *shell.Sandbox
	if ag := s.opts.Agent; ag != nil {
		sb = ag.Sandbox
	}
	status, err := shell.SetSandbox(sb, arg)
	if err != nil {
		s.errf("%v", err)
		return
	}
	s.notice(status)
}

func (s *server) retarget(kind, arg string, dial func(string) (tool.Host, error)) {
	status, err := tool.Retarget(s.opts.Workspace, s.cwd(), kind, arg, dial)
	if err != nil {
		s.errf("%v", err)
		return
	}
	s.notice(status)
}
