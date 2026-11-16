package memory

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xmonader/janetcode/internal/fsutil"
)

// Automatic memory (the extractor after a turn, the sleep pass after a
// goal) can be switched off two ways, and either one stops it:
//
//   - for this session, with /memory auto off (AutoSession);
//   - for this project, with /memory auto off project, which writes a marker
//     file next to the project's entries so every later session, in any
//     front end, keeps it off until /memory auto on project removes it.
//
// What the user writes with /remember and the model's own memory tool calls
// are separate: they are explicit actions, and memory_confirm governs them.

// autoOffFile is the marker inside the project's memory directory.
const autoOffFile = "auto-off"

// autoOffPath is the project's marker; it follows /cd to another project.
func (s *Store) autoOffPath() string { return filepath.Join(s.Root, s.ProjectKey(), autoOffFile) }

// AutoProjectOff reports whether the project has automatic memory switched
// off.
func (s *Store) AutoProjectOff() bool {
	_, err := os.Stat(s.autoOffPath())
	return err == nil
}

// AutoAllowed reports whether the extractor and the sleep pass may write
// memory now: neither the session nor the project has switched them off.
func (s *Store) AutoAllowed() bool {
	return s != nil && !s.AutoSession.Load() && !s.AutoProjectOff()
}

// SetAutoProject writes or removes the project's marker.
func (s *Store) SetAutoProject(on bool) error {
	p := s.autoOffPath()
	if on {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	return fsutil.AtomicWriteMode(p, []byte("automatic memory is off for this project (/memory auto on project)\n"), fsutil.PrivateFileMode)
}

// AutoStatus describes where automatic memory stands and what changes it.
func (s *Store) AutoStatus() string {
	switch {
	case s.AutoProjectOff() && s.AutoSession.Load():
		return "automatic memory is off for this session and for this project (/memory auto on project lifts the project's switch)"
	case s.AutoProjectOff():
		return "automatic memory is off for this project: nothing is extracted or consolidated here in any session (/memory auto on project turns it back on)"
	case s.AutoSession.Load():
		return "automatic memory is off for this session (/memory auto on turns it back on; /memory auto off project switches it off for every session)"
	}
	return "automatic memory is on: facts are extracted after turns that change something or give feedback (/memory auto off for this session, /memory auto off project for this project)"
}

// autoCommand is /memory auto [on|off] [project].
func (s *Store) autoCommand(rest string) (string, error) {
	f := strings.Fields(rest)
	usage := fmt.Errorf("usage: /memory auto [on|off] [project]")
	if len(f) > 2 || (len(f) == 2 && f[1] != "project") {
		return "", usage
	}
	if len(f) > 0 {
		var on bool
		switch f[0] {
		case "on":
			on = true
		case "off":
		default:
			return "", usage
		}
		if len(f) == 2 {
			if err := s.SetAutoProject(on); err != nil {
				return "", fmt.Errorf("memory auto: %w", err)
			}
		} else {
			s.AutoSession.Store(!on)
		}
	}
	return s.AutoStatus(), nil
}
