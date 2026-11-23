package tool

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/xmonader/janetcode/internal/textfmt"
)

// A model writes paths from memory, and three slips recur: a "~" it
// expects the shell to expand (the file tools never see a shell), a
// leading slash dropped from an absolute path ("home/me/proj/x.go"), and
// the workspace's own name repeated in front of a path that is already
// relative to it ("proj/x.go" when the working directory is proj). Each
// has exactly one sensible reading, and refusing it costs a round trip.
//
// The repair only rewrites the string the model sent. Confinement, the
// ignore rules and admission all run afterwards on the repaired path, in
// the tool's own resolve, so a repair can never widen what a call may
// touch: "~/x" outside the working directory is refused as before, just
// under its real name.

// RepairPaths returns input with its "path" and "paths" values repaired,
// or input itself when nothing needed it. It is a pure rewrite of the
// arguments: no tool runs and no model is asked.
func RepairPaths(ctx context.Context, ws *Workspace, root string, input json.RawMessage) json.RawMessage {
	var obj map[string]json.RawMessage
	if len(input) == 0 || json.Unmarshal(input, &obj) != nil {
		return input
	}
	local := ws == nil || ws.Local()
	root = rootOf(root, ws)
	fix := func(p string) string { return repairPath(ctx, ws, root, p, local) }
	changed := false
	if raw, ok := obj["path"]; ok {
		var p string
		if json.Unmarshal(raw, &p) == nil {
			if q := fix(p); q != p {
				obj["path"], _ = json.Marshal(q)
				changed = true
			}
		}
	}
	if raw, ok := obj["paths"]; ok {
		var ps []string
		if json.Unmarshal(raw, &ps) == nil {
			for i, p := range ps {
				if q := fix(p); q != p {
					ps[i] = q
					changed = true
				}
			}
			if changed {
				obj["paths"], _ = json.Marshal(ps)
			}
		}
	}
	if !changed {
		return input
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return input
	}
	return out
}

func repairPath(ctx context.Context, ws *Workspace, root, p string, local bool) string {
	if p == "" {
		return p
	}
	if local {
		if home, err := os.UserHomeDir(); err == nil && home != "" && home != "/" {
			switch {
			case p == "~":
				return home
			case strings.HasPrefix(p, "~/"):
				return filepath.Join(home, p[2:])
			}
			// "home/me/x" is only an absolute path with its slash
			// dropped when it names the user's own home: any other
			// "home/..." may well be a directory in the project.
			bare := strings.TrimPrefix(home, "/")
			if !filepath.IsAbs(p) && (p == bare || strings.HasPrefix(p, bare+"/")) {
				if _, err := os.Stat(filepath.Join(root, p)); err != nil {
					return "/" + p
				}
			}
		}
	}
	if root != "" && !filepath.IsAbs(p) {
		base := filepath.Base(root)
		if rest, ok := strings.CutPrefix(p, base+"/"); ok && rest != "" {
			_, errGiven := statOf(ctx, ws, filepath.Join(root, p))
			_, errRest := statOf(ctx, ws, filepath.Join(root, rest))
			if errGiven != nil && errRest == nil {
				return rest
			}
		}
	}
	return p
}

// didYouMean names the entry of the missing path's parent directory that
// is closest to what was typed, as a sentence to append to the error, or
// "" when the parent is missing or nothing is close.
func didYouMean(ctx context.Context, ws *Workspace, root, p string) string {
	abs := p
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(rootOf(root, ws), p)
	}
	var entries []fs.DirEntry
	var err error
	if ws != nil {
		entries, err = ws.Host().ReadDir(ctx, filepath.Dir(abs))
	} else {
		entries, err = os.ReadDir(filepath.Dir(abs))
	}
	if err != nil || len(entries) == 0 {
		return ""
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if near := textfmt.NearestName(filepath.Base(abs), names); near != "" && near != filepath.Base(abs) {
		return "did you mean " + near + "?"
	}
	return ""
}

// notFoundHint is the error for a missing path, with the suggestion when
// there is one and the repair is on. Callers pass it only an error that
// already means "does not exist".
func notFoundHint(ctx context.Context, ws *Workspace, root, p string, off bool, base error) error {
	if off {
		return base
	}
	if h := didYouMean(ctx, ws, root, p); h != "" {
		return errors.New(base.Error() + "; " + h)
	}
	return base
}
