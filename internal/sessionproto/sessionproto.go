// Package sessionproto is the contract between the clients of one session
// and the process that runs it: who may change it, who may only watch,
// and what a client is told when it asks for something it may not have.
//
// It imports nothing from janetcode, so the web UI, ACP and the CLI can
// speak it without knowing anything about the agent or the session store.
// The wire form is documented in docs/manual/protocol.md; Version names
// that form, and a change a client could notice is a new Version.
//
// Two levels of ownership exist and they are different things:
//
//   - The process lease (session.Acquire) says which process runs turns in
//     a session. It is an OS lock, so the kernel drops it when the process
//     dies. This package does not replace it.
//   - The control lease here says which client of that process may change
//     the session. Any number of clients may watch.
package sessionproto

import (
	"fmt"
	"sync"
	"time"
)

// Version is the protocol version. Clients send it as X-Janet-Protocol; a
// server answers a client that names a different one with CodeBadVersion.
const Version = 1

// Conflict codes: the Code of a Conflict, stable across versions.
const (
	// CodeLeaseHeld: another client controls the session and its lease
	// is live.
	CodeLeaseHeld = "lease_held"
	// CodeStaleLease: the client acted under a lease epoch that is over
	// (it expired, was taken over, or the session changed under it).
	CodeStaleLease = "stale_lease"
	// CodeObserver: the client attached as an observer, which may not
	// change anything.
	CodeObserver = "observer_readonly"
	// CodeBusy: a handoff was asked for while the holder's turn runs;
	// handoffs happen at turn boundaries.
	CodeBusy = "turn_running"
	// CodeBadVersion: the client speaks a protocol version the server
	// does not.
	CodeBadVersion = "bad_version"
)

// Role is what a client does in a session.
type Role string

const (
	RoleController Role = "controller"
	RoleObserver   Role = "observer"
	// RoleNone: attached to nothing, or not yet decided. A client with no
	// role that asks for a change becomes the controller if nobody is.
	RoleNone Role = "none"
)

// Holder is the client that controls a session.
type Holder struct {
	Client string    `json:"client"`
	Name   string    `json:"name,omitempty"`
	Since  time.Time `json:"since"`
	// Expires is when the lease lapses unless the holder is heard from.
	Expires time.Time `json:"expires"`
}

// Snapshot is the control state of a session: what a client is shown on
// attach, after every change, and inside a Conflict.
type Snapshot struct {
	Protocol int `json:"protocol"`
	// Epoch counts grants of the control lease. It only goes up, and it
	// is what a client presents to show the lease it believes it holds.
	Epoch     uint64  `json:"epoch"`
	Holder    *Holder `json:"holder,omitempty"`
	Observers int     `json:"observers"`
	// You is the asking client's role.
	You Role `json:"you,omitempty"`
	// Busy: a turn is running.
	Busy bool `json:"busy"`
	// Queued is how many prompts wait behind the running turn.
	Queued int `json:"queued"`
}

// Conflict is the refusal a client gets, with the state it should have
// acted on.
type Conflict struct {
	Code    string   `json:"code"`
	Message string   `json:"message"`
	State   Snapshot `json:"state"`
}

func (c *Conflict) Error() string { return c.Message }

// DefaultTTL is how long a control lease lives without its holder being
// heard from.
const DefaultTTL = 15 * time.Second

// Table is the control lease of one session. Use NewTable.
type Table struct {
	mu     sync.Mutex
	ttl    time.Duration
	now    func() time.Time
	holder *Holder
	epoch  uint64
	// watching is the clients that attached as observers and not since
	// took control, with how many attachments each has.
	watching map[string]int
}

// NewTable makes a table whose leases last ttl. now is the clock, time.Now
// when nil.
func NewTable(ttl time.Duration, now func() time.Time) *Table {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	if now == nil {
		now = time.Now
	}
	return &Table{ttl: ttl, now: now, watching: map[string]int{}}
}

// expireLocked drops a lapsed holder.
func (t *Table) expireLocked() {
	if t.holder != nil && !t.now().Before(t.holder.Expires) {
		t.holder = nil
	}
}

func (t *Table) snapshotLocked(client string, busy bool, queued int) Snapshot {
	s := Snapshot{Protocol: Version, Epoch: t.epoch, Busy: busy, Queued: queued, You: RoleNone}
	if t.holder != nil {
		h := *t.holder
		s.Holder = &h
		if h.Client == client {
			s.You = RoleController
		}
	}
	for _, n := range t.watching {
		if n > 0 {
			s.Observers++
		}
	}
	if s.You == RoleNone && t.watching[client] > 0 {
		s.You = RoleObserver
	}
	return s
}

// View is the control state as client sees it.
func (t *Table) View(client string, busy bool, queued int) Snapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.expireLocked()
	return t.snapshotLocked(client, busy, queued)
}

func (t *Table) conflictLocked(code, msg, client string, busy bool, queued int) *Conflict {
	return &Conflict{Code: code, Message: msg, State: t.snapshotLocked(client, busy, queued)}
}

func (t *Table) grantLocked(client, name string) {
	now := t.now()
	t.epoch++
	t.holder = &Holder{Client: client, Name: name, Since: now, Expires: now.Add(t.ttl)}
	delete(t.watching, client)
}

// holderLabel is how a refusal names the holder.
func (t *Table) holderLabel() string {
	if t.holder.Name != "" {
		return t.holder.Name
	}
	return t.holder.Client
}

// Acquire makes client the controller. It succeeds when nobody controls
// the session, when the lease lapsed, or when client already holds it
// (which renews it). Otherwise it is a CodeLeaseHeld conflict.
func (t *Table) Acquire(client, name string, busy bool, queued int) (Snapshot, *Conflict) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.expireLocked()
	switch {
	case t.holder == nil:
		t.grantLocked(client, name)
	case t.holder.Client == client:
		t.holder.Expires = t.now().Add(t.ttl)
	default:
		return Snapshot{}, t.conflictLocked(CodeLeaseHeld,
			fmt.Sprintf("this session is controlled by %s; watch it, or take control when its turn is done", t.holderLabel()),
			client, busy, queued)
	}
	return t.snapshotLocked(client, busy, queued), nil
}

// Take is a handoff: client becomes the controller although another
// client holds a live lease, but only at a turn boundary. While a turn
// runs it is a CodeBusy conflict.
func (t *Table) Take(client, name string, busy bool, queued int) (Snapshot, *Conflict) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.expireLocked()
	switch {
	case t.holder == nil:
		t.grantLocked(client, name)
	case t.holder.Client == client:
		t.holder.Expires = t.now().Add(t.ttl)
	case busy:
		return Snapshot{}, t.conflictLocked(CodeBusy,
			fmt.Sprintf("%s is running a turn; control can change hands when it finishes", t.holderLabel()),
			client, busy, queued)
	default:
		t.grantLocked(client, name)
	}
	return t.snapshotLocked(client, busy, queued), nil
}

// Release gives up the lease if client holds it. It reports whether it did.
func (t *Table) Release(client string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.expireLocked()
	if t.holder != nil && t.holder.Client == client {
		t.holder = nil
		return true
	}
	return false
}

// Touch renews client's lease if it holds one: the heartbeat of an open
// stream. It reports whether client holds a live lease.
func (t *Table) Touch(client string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.expireLocked()
	if t.holder != nil && t.holder.Client == client {
		t.holder.Expires = t.now().Add(t.ttl)
		return true
	}
	return false
}

// Watch registers an observer attachment and returns the function that
// ends it. An observer never holds the lease; asking for a change as an
// observer is a CodeObserver conflict until it calls Acquire or Take.
func (t *Table) Watch(client string) (leave func()) {
	t.mu.Lock()
	t.watching[client]++
	t.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			t.mu.Lock()
			if t.watching[client] > 1 {
				t.watching[client]--
			} else {
				delete(t.watching, client)
			}
			t.mu.Unlock()
		})
	}
}

// Authorize is the check every change to the session goes through. epoch
// is the lease epoch the client says it acts under, 0 when it says
// nothing. A client that is neither holder nor observer becomes the
// controller if the session is free: asking is enough, so a plain client
// that never heard of leases still works until a second client appears.
func (t *Table) Authorize(client, name string, epoch uint64, busy bool, queued int) *Conflict {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.expireLocked()
	stale := epoch != 0 && epoch != t.epoch
	if t.holder != nil && t.holder.Client == client {
		if stale {
			return t.conflictLocked(CodeStaleLease,
				fmt.Sprintf("your lease is out of date (epoch %d, now %d); re-read the session state and ask again", epoch, t.epoch),
				client, busy, queued)
		}
		t.holder.Expires = t.now().Add(t.ttl)
		return nil
	}
	if t.watching[client] > 0 {
		return t.conflictLocked(CodeObserver, "this client is an observer and cannot change the session; take control first", client, busy, queued)
	}
	if t.holder != nil {
		if stale {
			return t.conflictLocked(CodeStaleLease,
				fmt.Sprintf("your lease is over (epoch %d, now %d) and %s controls the session", epoch, t.epoch, t.holderLabel()),
				client, busy, queued)
		}
		return t.conflictLocked(CodeLeaseHeld, fmt.Sprintf("this session is controlled by %s", t.holderLabel()), client, busy, queued)
	}
	if stale {
		return t.conflictLocked(CodeStaleLease,
			fmt.Sprintf("your lease is over (epoch %d, now %d): it lapsed or the session changed; re-read the session state", epoch, t.epoch),
			client, busy, queued)
	}
	t.grantLocked(client, name)
	return nil
}

// Reset ends the lease: the process moved to another session, or is
// shutting down. The epoch advances, so every client's old lease is stale.
func (t *Table) Reset() {
	t.mu.Lock()
	t.holder = nil
	t.epoch++
	t.mu.Unlock()
}

// Same reports whether two snapshots say the same thing, ignoring when
// the lease lapses; a server uses it to send a control event only when
// something changed.
func (s Snapshot) Same(o Snapshot) bool {
	if s.Epoch != o.Epoch || s.Observers != o.Observers || s.You != o.You || s.Busy != o.Busy || s.Queued != o.Queued {
		return false
	}
	if (s.Holder == nil) != (o.Holder == nil) {
		return false
	}
	return s.Holder == nil || s.Holder.Client == o.Holder.Client
}
