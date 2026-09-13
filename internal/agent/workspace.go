package agent

import (
	"fmt"

	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

// AddDir grants ws an extra directory for the file tools and, when sess is
// not nil, persists the new list so it survives a resume.
func AddDir(ws *tool.Workspace, sess *session.Session, dir string) error {
	if ws == nil {
		return fmt.Errorf("this session has no workspace to add a directory to")
	}
	if err := ws.AddDir(dir); err != nil {
		return fmt.Errorf("add-dir: %v", err)
	}
	if sess != nil {
		_ = sess.SetExtraDirs(ws.ExtraDirs())
	}
	return nil
}
