// The workspace picker: a path typed into the page, checked before it is
// applied over the same /cd move the command makes.
package webui

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

type pathBody struct {
	Path   string `json:"path"`
	Create bool   `json:"create"`
}

// resolveWorkspacePath turns what was typed into an absolute path. Only
// absolute and ~ paths are taken: a relative one would mean whatever the
// server's own directory happens to be.
func resolveWorkspacePath(raw string) (string, error) {
	p := strings.TrimSpace(raw)
	if p == "" {
		return "", errors.New("workspace path required")
	}
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", errors.New("could not resolve ~")
		}
		p = filepath.Join(home, strings.TrimPrefix(p, "~"))
	}
	if !filepath.IsAbs(p) {
		return "", errors.New("use an absolute path or one starting with ~")
	}
	return filepath.Clean(p), nil
}

// checkWorkspace stats the path and, only when create is set and nothing
// is there, makes the directory. A file is refused either way.
func checkWorkspace(raw string, create bool) (map[string]any, int, error) {
	abs, err := resolveWorkspacePath(raw)
	if err != nil {
		return nil, http.StatusBadRequest, err
	}
	out := map[string]any{"path": abs, "exists": false, "is_dir": false, "created": false}
	info, err := os.Stat(abs)
	switch {
	case err == nil:
		out["exists"], out["is_dir"] = true, info.IsDir()
		if !info.IsDir() {
			return out, http.StatusBadRequest, errors.New(abs + " is a file, not a directory")
		}
	case errors.Is(err, os.ErrNotExist) && create:
		if err := os.MkdirAll(abs, 0o755); err != nil {
			return out, http.StatusInternalServerError, errors.New("could not create the directory: " + err.Error())
		}
		out["exists"], out["is_dir"], out["created"] = true, true, true
	case errors.Is(err, os.ErrNotExist):
	default:
		return out, http.StatusBadRequest, errors.New("could not access the path: " + err.Error())
	}
	return out, http.StatusOK, nil
}

func decodePath(w http.ResponseWriter, r *http.Request) (pathBody, bool) {
	var b pathBody
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return b, false
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&b); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return b, false
	}
	return b, true
}

// workspaceCheck answers whether the path exists and is a directory;
// with create it makes a missing one (the page's confirmed "Create folder?").
func (s *server) workspaceCheck(w http.ResponseWriter, r *http.Request) {
	b, ok := decodePath(w, r)
	if !ok {
		return
	}
	out, code, err := checkWorkspace(b.Path, b.Create)
	if err != nil {
		http.Error(w, err.Error(), code)
		return
	}
	writeJSON(w, out)
}

// workspaceApply moves the session to the path, creating it first only
// when asked to. Refused while a turn runs, as a /cd is.
func (s *server) workspaceApply(w http.ResponseWriter, r *http.Request) {
	b, ok := decodePath(w, r)
	if !ok {
		return
	}
	if s.opts.CD == nil {
		http.Error(w, "this session has no working directory to change", http.StatusConflict)
		return
	}
	if s.isRunning() {
		http.Error(w, turnRunning, http.StatusConflict)
		return
	}
	out, code, err := checkWorkspace(b.Path, b.Create)
	if err != nil {
		http.Error(w, err.Error(), code)
		return
	}
	if out["exists"] != true {
		http.Error(w, "that folder does not exist", http.StatusNotFound)
		return
	}
	status, err := s.opts.CD.Change(out["path"].(string))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.setCwd(s.opts.CD.Dir())
	s.notice(status)
	s.emit(envelope{Type: "hello", Hello: ptr(s.hello())})
	s.emitStatus()
	out["cwd"] = s.cwd()
	out["status"] = status
	writeJSON(w, out)
}
