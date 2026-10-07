package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// A session is driven by one process at a time. Two janetcode processes
// appending to one session file interleave their turns into a history
// neither of them wrote, and each one's undo stack, accept lock and loops
// are saved over the other's. So the process that runs turns in a session
// holds its lease: an OS lock on the <id>.lock sidecar (flock on unix,
// LockFileEx on Windows), which the kernel drops when the process dies,
// however it dies, so a crash never leaves a session locked. The file's
// contents say who holds it, for the refusal a second opener gets.
//
// Inside one process the lease is shared: the TUI's /resume of a session
// the web view of the same process has open is one owner, not two.

// LeaseInfo is who holds a session's lease.
type LeaseInfo struct {
	PID    int       `json:"pid"`
	Host   string    `json:"host,omitempty"`
	Holder string    `json:"holder,omitempty"`
	Since  time.Time `json:"since"`
}

// LeasedError is the refusal to open a session another process holds.
type LeasedError struct {
	ID   string
	Info LeaseInfo
}

func (e *LeasedError) Error() string {
	return fmt.Sprintf("session %s is open in another janetcode (%s); only one process can run turns in a session at a time", e.ID, e.Info.describe())
}

// describe is the holder as a phrase: "a terminal session, pid 4121 on
// box, since 14:02".
func (in LeaseInfo) describe() string {
	var parts []string
	if in.Holder != "" {
		parts = append(parts, in.Holder)
	}
	if in.PID > 0 {
		p := fmt.Sprintf("pid %d", in.PID)
		if in.Host != "" && in.Host != hostname() {
			p += " on " + in.Host
		}
		parts = append(parts, p)
	}
	if !in.Since.IsZero() {
		parts = append(parts, "since "+in.Since.Local().Format("Jan 2 15:04"))
	}
	if len(parts) == 0 {
		return "holder unknown"
	}
	return strings.Join(parts, ", ")
}

// alive reports whether the holder may still be running: a process on
// another host cannot be checked, so it counts as alive.
func (in LeaseInfo) alive() bool {
	if in.PID <= 0 {
		return false
	}
	if in.Host != "" && in.Host != hostname() {
		return true
	}
	return processAlive(in.PID)
}

var (
	holderMu   sync.Mutex
	holderName = "janetcode"
)

// SetHolder names what this process is ("the terminal interface", "the
// web interface", "a background run", ...), for the refusal another
// process's opener shows.
func SetHolder(name string) {
	holderMu.Lock()
	holderName = name
	holderMu.Unlock()
}

func holder() string {
	holderMu.Lock()
	defer holderMu.Unlock()
	return holderName
}

func hostname() string {
	h, _ := os.Hostname()
	return h
}

// LockPath is the lease sidecar of the session file at sessionPath.
func LockPath(sessionPath string) string { return sidecar(sessionPath, ".lock") }

// held is this process's leases, by lock path, with how many Session
// values share each.
var (
	heldMu sync.Mutex
	held   = map[string]*heldLease{}
)

type heldLease struct {
	f    *os.File
	refs int
}

// Acquire takes the session's lease for this process, or returns a
// *LeasedError when another live process holds it. Acquiring a lease the
// Session already holds is a no-op.
func (s *Session) Acquire() error {
	s.mu.Lock()
	if s.leased {
		s.mu.Unlock()
		return nil
	}
	path := LockPath(s.path)
	s.mu.Unlock()

	heldMu.Lock()
	defer heldMu.Unlock()
	if h := held[path]; h != nil {
		h.refs++
		s.setLeased(true)
		return nil
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("session lease: %w", err)
	}
	ok, lerr := tryLock(f)
	if lerr != nil {
		// A filesystem without locks: fall back to the recorded holder,
		// taken over when its process is gone.
		if in, rerr := readLease(f); rerr == nil && in.alive() && !(in.PID == os.Getpid() && in.Host == hostname()) {
			f.Close()
			return &LeasedError{ID: s.Meta.ID, Info: in}
		}
	} else if !ok {
		in, _ := readLease(f)
		f.Close()
		return &LeasedError{ID: s.Meta.ID, Info: in}
	}
	b, _ := json.Marshal(LeaseInfo{PID: os.Getpid(), Host: hostname(), Holder: holder(), Since: time.Now()})
	_ = f.Truncate(0)
	_, _ = f.WriteAt(b, 0)
	held[path] = &heldLease{f: f, refs: 1}
	s.setLeased(true)
	return nil
}

func (s *Session) setLeased(v bool) {
	s.mu.Lock()
	s.leased = v
	s.mu.Unlock()
}

// Leased reports whether this Session holds its lease.
func (s *Session) Leased() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.leased
}

// Release gives the lease back. The lock file stays: removing it would let
// a process that opened the old file lock it while a third locks a new one.
func (s *Session) Release() {
	s.mu.Lock()
	if !s.leased {
		s.mu.Unlock()
		return
	}
	s.leased = false
	path := LockPath(s.path)
	s.mu.Unlock()

	heldMu.Lock()
	defer heldMu.Unlock()
	h := held[path]
	if h == nil {
		return
	}
	if h.refs--; h.refs > 0 {
		return
	}
	delete(held, path)
	_ = h.f.Truncate(0)
	unlock(h.f)
	h.f.Close()
}

// Holder reports who holds the lease of the session file at sessionPath,
// with ok false when nobody live does.
func Holder(sessionPath string) (LeaseInfo, bool) {
	path := LockPath(sessionPath)
	heldMu.Lock()
	if held[path] != nil {
		heldMu.Unlock()
		return LeaseInfo{PID: os.Getpid(), Host: hostname(), Holder: holder()}, true
	}
	heldMu.Unlock()
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return LeaseInfo{}, false
	}
	defer f.Close()
	ok, lerr := tryLock(f)
	if lerr == nil && ok {
		unlock(f)
		return LeaseInfo{}, false
	}
	in, err := readLease(f)
	if err != nil || (lerr != nil && !in.alive()) {
		return LeaseInfo{}, false
	}
	return in, true
}

func readLease(f *os.File) (LeaseInfo, error) {
	var in LeaseInfo
	buf := make([]byte, 4096)
	n, err := f.ReadAt(buf, 0)
	if n == 0 {
		if err == nil {
			err = errors.New("empty lease")
		}
		return in, err
	}
	return in, json.Unmarshal(buf[:n], &in)
}
