package agent

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

// Rooted is one part of a session that is rooted at the working directory
// and therefore has to follow a /cd. Move is given the new directory and
// returns a short note for the report ("" for the ordinary case, where
// the part simply moved) or an error when it could not move at all.
//
// A part that genuinely cannot move — an open language server, a command
// already running — moves what it can and says the rest in its note. The
// one thing it may not do is stay silently pointed at the old tree.
type Rooted struct {
	Name string
	Move func(dir string) (note string, err error)
}

// CD is the session's working directory and everything rooted at it.
//
// /cd is the local form of the workspace retarget /ssh and /docker
// already do: it moves the same tool.Workspace, so the file tools and
// bash follow for free, and then moves the parts of the session that are
// rooted at the project rather than at the host — the repository map, the
// project's hooks and verification files, skills, rules and memory, the
// plan file, the language servers, the subagents' worktree root.
//
// The conversation is not touched. A move is a move, not a new session:
// the model is told once, through the harness-note path, and the session
// metadata records the new directory so --resume comes back to it.
type CD struct {
	mu     sync.Mutex
	dir    string
	ws     *tool.Workspace
	ag     *Agent
	sess   *session.Session
	rooted []Rooted
}

// NewCD returns the tracker for a session rooted at dir.
func NewCD(dir string) *CD { return &CD{dir: dir} }

// Dir is where the session is rooted now. Every part of the wiring that
// reads the project directory lazily reads it from here, so it is correct
// after a move without being re-registered.
func (c *CD) Dir() string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dir
}

// Bind gives the tracker the three things it moves itself: the workspace
// the tools run through, the agent whose Root roots the plan file and the
// paths shown to the user, and the session whose metadata records where
// a resume should come back to. Any of them may be nil.
func (c *CD) Bind(ws *tool.Workspace, ag *Agent, sess *session.Session) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ws, c.ag, c.sess = ws, ag, sess
}

// SetSession points the tracker at the session a resume or /new opened.
func (c *CD) SetSession(sess *session.Session) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sess = sess
}

// Add registers one more thing rooted at the working directory. Order is
// the order of registration, so the report reads in the order the
// composition root wired the session up.
func (c *CD) Add(name string, move func(dir string) (string, error)) *CD {
	if c == nil {
		return c
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rooted = append(c.rooted, Rooted{Name: name, Move: move})
	return c
}

// Consumers names everything registered, for a report and for the test
// that asserts no consumer of the root was left behind.
func (c *CD) Consumers() []string {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	names := make([]string, 0, len(c.rooted))
	for _, r := range c.rooted {
		names = append(names, r.Name)
	}
	sort.Strings(names)
	return names
}

// Where is the no-argument report: where the session is rooted, what else
// the file tools may touch, and that the remote forms are separate.
func (c *CD) Where() string {
	if c == nil {
		return "this session has no working directory"
	}
	dir := c.Dir()
	var b strings.Builder
	fmt.Fprintf(&b, "rooted at %s", dir)
	c.mu.Lock()
	ws := c.ws
	c.mu.Unlock()
	if ws != nil {
		if !ws.Local() {
			fmt.Fprintf(&b, "\nfile tools and bash run on %s", ws.Label())
		}
		if extra := ws.ExtraDirs(); len(extra) > 0 {
			fmt.Fprintf(&b, "\nalso readable: %s", strings.Join(extra, ", "))
		}
	}
	b.WriteString("\n/cd <path> moves the session there; /ssh and /docker are the remote forms")
	return b.String()
}

// Change applies a /cd argument. An empty argument (or "status") reports
// where the session is rooted instead of moving it.
func (c *CD) Change(arg string) (string, error) {
	if c == nil {
		return "", fmt.Errorf("this session has no working directory to change")
	}
	switch strings.TrimSpace(arg) {
	case "", "status":
		return c.Where(), nil
	}
	c.mu.Lock()
	ws, ag, sess, rooted, cur := c.ws, c.ag, c.sess, append([]Rooted(nil), c.rooted...), c.dir
	c.mu.Unlock()

	// /cd is the local form of the retarget. Moving the project root while
	// the tools are on another machine would leave the session with two
	// working directories and no way to say which one a path means, so it
	// is refused rather than guessed at.
	if ws != nil && !ws.Local() {
		return "", fmt.Errorf("the workspace is on %s: bring it back with /ssh off (or /docker off) before /cd, or move it with /ssh user@host:/path", ws.Label())
	}
	dir, err := tool.ResolveDir(cur, arg)
	if err != nil {
		return "", fmt.Errorf("cd: %w", err)
	}
	if dir == cur {
		return "already rooted at " + dir, nil
	}

	c.mu.Lock()
	c.dir = dir
	c.mu.Unlock()
	if ws != nil {
		ws.Set(tool.LocalHost(dir))
	}
	if ag != nil {
		ag.SetRoot(dir)
	}
	var notes []string
	for _, r := range rooted {
		if r.Move == nil {
			continue
		}
		note, err := r.Move(dir)
		switch {
		case err != nil:
			notes = append(notes, r.Name+": "+err.Error())
		case note != "":
			notes = append(notes, note)
		}
	}
	if sess != nil {
		if err := sess.SetCwd(dir); err != nil {
			notes = append(notes, "session metadata: "+err.Error())
		}
	}
	if ag != nil {
		ag.AddHarnessContext(cdNote(cur, dir))
	}
	status := "cd " + dir
	if len(notes) > 0 {
		status += " · " + strings.Join(notes, " · ")
	}
	return status, nil
}

// cdNote is what the model is told, once, when the session moves. It is
// deliberately short: the working directory the model actually works from
// is the one in the system prompt's environment section, which is rebuilt
// for every request, and this only has to stop the model trusting paths it
// already has in front of it.
func cdNote(from, to string) string {
	return fmt.Sprintf("[harness] The user ran /cd: this session's working directory moved from %s to %s. "+
		"It is the same conversation, but the file tools, bash and every relative path now resolve under the new directory, "+
		"and the project's own files (its instructions, hooks, verification config, skills and rules) are read from there. "+
		"Paths you gathered under the old directory are no longer valid as relative paths; look things up again rather than assuming they carried over.\n", from, to)
}

// SetRoot moves the directory the session is rooted at. /cd calls it
// while a turn may be reading Root, so the write goes through the same
// lock the other live-changeable settings use.
func (a *Agent) SetRoot(dir string) {
	a.settingsMu.Lock()
	defer a.settingsMu.Unlock()
	a.Root = dir
}

// RootDir reads Root under that lock.
func (a *Agent) RootDir() string {
	a.settingsMu.Lock()
	defer a.settingsMu.Unlock()
	return a.Root
}

// SetSystem replaces the base system prompt. /cd rebuilds it because its
// environment section names the working directory, the git branch and the
// project's own instruction files, all of which change with the move.
func (a *Agent) SetSystem(text string) {
	a.settingsMu.Lock()
	a.System = text
	a.settingsMu.Unlock()
	// The memory and skill indexes below it are keyed by project too.
	a.RefreshExtra()
}

// SystemPrompt reads the base system prompt under that lock.
func (a *Agent) SystemPrompt() string {
	a.settingsMu.Lock()
	defer a.settingsMu.Unlock()
	return a.System
}
