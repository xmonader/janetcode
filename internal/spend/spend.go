// Package spend keeps a persistent record of what janetcode has spent per
// local day and month, and refuses model calls once a configured cap is
// reached. --max-cost bounds one run; a person running many short
// sessions, scripts and background jobs has no bound on the day, and the
// invoice is where they find out. The ledger is shared by every janetcode
// process on the machine, so the cap is on the account's spend and not on
// whichever process happens to be asking.
package spend

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/xdg"
)

// File is the ledger's name inside the state directory.
const File = "spend.json"

// Retention bounds the file: older days and months are of no use to a cap
// and would only make every call read and rewrite more.
const (
	keepDays   = 62
	keepMonths = 13
)

// Caps are the limits in USD; zero is off.
type Caps struct {
	Daily   float64
	Monthly float64
}

// On reports whether any cap is set.
func (c Caps) On() bool { return c.Daily > 0 || c.Monthly > 0 }

// CapError is a refusal because a cap has been reached. It names the cap
// and what has been spent so far, so the person can tell which limit to
// raise and whether it is worth raising.
type CapError struct {
	Period string // "daily" or "monthly"
	Cap    float64
	Spent  float64
}

func (e *CapError) Error() string {
	when := "today"
	if e.Period == "monthly" {
		when = "this month"
	}
	return fmt.Sprintf("%s spend cap of %s reached: %s spent %s (spend.%s_usd)",
		e.Period, llm.FormatUSDPrecise(e.Cap), llm.FormatUSDPrecise(e.Spent), when, periodKey(e.Period))
}

func periodKey(p string) string {
	if p == "monthly" {
		return "monthly"
	}
	return "daily"
}

// data is the file's content. Keys are local dates ("2006-01-02") and
// months ("2006-01"), which sort the way time does.
type data struct {
	Days   map[string]float64 `json:"days"`
	Months map[string]float64 `json:"months"`
}

// Ledger is the on-disk record. Every method re-reads the file, so two
// Ledgers on one path (two processes, or a restart) see each other.
type Ledger struct {
	path string
	now  func() time.Time
	mu   sync.Mutex
}

// New opens the ledger at path. now supplies the clock; nil is time.Now.
// The file is created on the first Add.
func New(path string, now func() time.Time) *Ledger {
	if now == nil {
		now = time.Now
	}
	return &Ledger{path: path, now: now}
}

// DefaultPath is the ledger under the XDG state directory, so
// XDG_STATE_HOME moves it and a test never has to touch the real one.
func DefaultPath(getenv func(string) string) (string, error) {
	dir, err := xdg.StateDir(getenv)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, File), nil
}

func (l *Ledger) keys() (day, month string) {
	t := l.now()
	return t.Format("2006-01-02"), t.Format("2006-01")
}

// Totals is the spend so far today and this month.
func (l *Ledger) Totals() (day, month float64, err error) {
	d, err := l.read()
	dk, mk := l.keys()
	return d.Days[dk], d.Months[mk], err
}

// Check returns a *CapError when a cap is already reached. A ledger that
// cannot be read returns the read error beside a zero spend and no
// refusal: an unreadable file is not a reason to stop working.
func (l *Ledger) Check(c Caps) error {
	if !c.On() {
		return nil
	}
	day, month, err := l.Totals()
	if err != nil {
		return nil
	}
	if c.Daily > 0 && day >= c.Daily {
		return &CapError{Period: "daily", Cap: c.Daily, Spent: day}
	}
	if c.Monthly > 0 && month >= c.Monthly {
		return &CapError{Period: "monthly", Cap: c.Monthly, Spent: month}
	}
	return nil
}

// Add records usd against today and this month. The read-modify-write is
// held under a lock file, so concurrent processes do not lose each
// other's spend, and the result is written by rename, so a reader never
// sees half a file.
func (l *Ledger) Add(usd float64) error {
	if !(usd > 0) || math.IsInf(usd, 0) {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
		return err
	}
	unlock, err := lockFile(l.path + ".lock")
	if err != nil {
		return err
	}
	defer unlock()
	d, err := l.read()
	if err != nil {
		// A corrupt file is started over rather than wedging the feature
		// for good; the spend it held is lost, which errs toward working.
		d = data{Days: map[string]float64{}, Months: map[string]float64{}}
	}
	dk, mk := l.keys()
	d.Days[dk] += usd
	d.Months[mk] += usd
	prune(d.Days, keepDays)
	prune(d.Months, keepMonths)
	return l.write(d)
}

func (l *Ledger) read() (data, error) {
	d := data{Days: map[string]float64{}, Months: map[string]float64{}}
	b, err := os.ReadFile(l.path)
	if errors.Is(err, os.ErrNotExist) {
		return d, nil
	}
	if err != nil {
		return d, err
	}
	if err := json.Unmarshal(b, &d); err != nil {
		return data{Days: map[string]float64{}, Months: map[string]float64{}}, err
	}
	if d.Days == nil {
		d.Days = map[string]float64{}
	}
	if d.Months == nil {
		d.Months = map[string]float64{}
	}
	return d, nil
}

func (l *Ledger) write(d data) error {
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(l.path), File+".*.tmp")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(b)
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), l.path); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}

func prune(m map[string]float64, keep int) {
	if len(m) <= keep {
		return
	}
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	for _, k := range ks[:len(ks)-keep] {
		delete(m, k)
	}
}

// Lock-file tuning: a holder keeps the lock for one small file rewrite,
// so a lock older than staleAfter belongs to a process that died.
const (
	lockWait   = 3 * time.Second
	staleAfter = 10 * time.Second
)

// lockFile takes an exclusive lock by creating path, which works the same
// on every platform, and returns its release. It gives up after lockWait:
// recording spend must never hang a model call.
func lockFile(path string) (func(), error) {
	deadline := time.Now().Add(lockWait)
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			f.Close()
			return func() { os.Remove(path) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if st, serr := os.Stat(path); serr == nil && time.Since(st.ModTime()) > staleAfter {
			os.Remove(path)
			continue
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("spend ledger lock %s is held", path)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Guard is what the agent holds: the ledger and the caps it enforces.
// A nil Guard is off, so callers need no check of their own.
type Guard struct {
	Ledger *Ledger
	Caps   Caps
}

// Summary is one line for a status display: today's and this month's
// spend, each beside its cap when there is one.
func (g *Guard) Summary() string {
	if g == nil {
		return ""
	}
	day, month, err := g.Ledger.Totals()
	if err != nil {
		return ""
	}
	part := func(name string, spent, cap float64) string {
		s := name + " " + llm.FormatUSDPrecise(spent)
		if cap > 0 {
			s += " of " + llm.FormatUSDPrecise(cap)
		}
		return s
	}
	return part("today", day, g.Caps.Daily) + " · " + part("this month", month, g.Caps.Monthly)
}

// Check refuses when a cap is reached.
func (g *Guard) Check() error {
	if g == nil {
		return nil
	}
	return g.Ledger.Check(g.Caps)
}

// Add records a call's cost. The error is for logging; it never blocks.
func (g *Guard) Add(usd float64) error {
	if g == nil {
		return nil
	}
	return g.Ledger.Add(usd)
}
