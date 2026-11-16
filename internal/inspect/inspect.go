// Package inspect makes the harness answerable about itself. Every part
// of a session — the context that will be sent, the archive behind it,
// the event log, the tools, the servers, the configuration and where each
// setting came from — registers a subject here, and one command reports
// any of them.
//
// The point is that a session should never have to be guessed at. When an
// agent does something surprising, the state that produced it is a
// question away, in the same words for every subsystem, from the terminal,
// the command line, or the model itself.
package inspect

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/xmonader/janetcode/internal/textfmt"
)

// Subject is one thing that can be looked at.
type Subject struct {
	// Name is what it is asked for by: one lowercase word.
	Name string
	// Summary is the one line shown when subjects are listed.
	Summary string
	// Usage describes the argument, when the subject takes one.
	Usage string
	// Report renders the subject. arg is what followed the name, or "".
	Report func(ctx context.Context, arg string) (string, error)
}

// Registry holds the subjects of one session.
type Registry struct {
	mu       sync.RWMutex
	subjects map[string]Subject
}

// New returns an empty registry.
func New() *Registry { return &Registry{subjects: map[string]Subject{}} }

// Add registers a subject, replacing one of the same name. A subject with
// no name or no report is ignored, so a caller can register a subsystem
// unconditionally and let it decline when it is not configured.
func (r *Registry) Add(s Subject) {
	if s.Name == "" || s.Report == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.subjects[s.Name] = s
}

// AddText registers a subject whose report needs no context or argument.
func (r *Registry) AddText(name, summary string, report func() string) {
	r.Add(Subject{Name: name, Summary: summary, Report: func(context.Context, string) (string, error) {
		return report(), nil
	}})
}

// Names lists the registered subjects in order.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.subjects))
	for n := range r.subjects {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Subjects lists the registered subjects with their summaries, in order.
func (r *Registry) Subjects() []Subject {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Subject, 0, len(r.subjects))
	for _, s := range r.subjects {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// NoSubjectError is Report's answer to a name it does not know. Its
// message is for the slash command; a command line words it itself.
type NoSubjectError struct {
	Name string
	Near string // the nearest known name, or ""
}

func (e *NoSubjectError) Error() string {
	if e.Near != "" {
		return fmt.Sprintf("no subject %q; did you mean %q? (inspect list shows them all)", e.Name, e.Near)
	}
	return fmt.Sprintf("no subject %q (inspect list shows them all)", e.Name)
}

// Report renders one subject. The request is the subject's name followed
// by its argument, as it is typed. An unknown name is answered with the
// list, and with the nearest name when there is an obvious one.
func (r *Registry) Report(ctx context.Context, request string) (string, error) {
	name, arg, _ := strings.Cut(strings.TrimSpace(request), " ")
	name = strings.ToLower(strings.TrimSpace(name))
	arg = strings.TrimSpace(arg)
	if name == "" || name == "list" {
		return r.List(), nil
	}
	r.mu.RLock()
	s, ok := r.subjects[name]
	r.mu.RUnlock()
	if !ok {
		return "", &NoSubjectError{Name: name, Near: r.nearest(name)}
	}
	out, err := s.Report(ctx, arg)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(out) == "" {
		return "(" + s.Name + " has nothing to report)", nil
	}
	return out, nil
}

// List renders the registered subjects as a table.
func (r *Registry) List() string {
	subs := r.Subjects()
	if len(subs) == 0 {
		return "nothing is registered for inspection"
	}
	width := 0
	for _, s := range subs {
		n := len(s.Name) + len(s.Usage)
		if s.Usage != "" {
			n++
		}
		if n > width {
			width = n
		}
	}
	var b strings.Builder
	b.WriteString("inspect <subject>:\n")
	for _, s := range subs {
		name := s.Name
		if s.Usage != "" {
			name += " " + s.Usage
		}
		fmt.Fprintf(&b, "  %-*s  %s\n", width, name, s.Summary)
	}
	return strings.TrimRight(b.String(), "\n")
}

// nearest is the registered name closest to what was asked for, when one
// is close enough to be worth suggesting.
func (r *Registry) nearest(name string) string {
	return textfmt.Nearest(name, r.Names())
}

// DoctorSubject maps what follows /doctor to the subject that answers
// it, so the terminal and the browser route "/doctor mcp" the same way
// and show what "/inspect extensions" shows. The request is in the form
// Registry.Report takes. ok is false for a bare /doctor and for any
// topic that is not one of these, which keep the ordinary checkup.
func DoctorSubject(arg string) (request string, ok bool) {
	topic, rest, _ := strings.Cut(strings.TrimSpace(arg), " ")
	switch strings.ToLower(topic) {
	case "mcp", "extensions":
		return strings.TrimSpace("extensions " + rest), true
	}
	return "", false
}
