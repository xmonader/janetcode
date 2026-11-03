package headless

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/present"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/shell"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// BgEnv marks a process started by --bg: it has no terminal to ask on,
// so its permission prompts wait in the session's status sidecar.
const BgEnv = "JANETCODE_BG"

// NotifyEnv names a command run each time a background run starts
// waiting for an answer, so the answer can come from wherever you are:
// a curl to ntfy or a Telegram bot, a desktop notifier. It runs through
// the shell with JANETCODE_SESSION, JANETCODE_ASK and JANETCODE_ANSWER
// (the command that answers) in its environment.
const NotifyEnv = "JANETCODE_NOTIFY"

// InboxApprover is the approver of a background run. Each request is
// written to the status sidecar and the run waits, with the process
// otherwise idle, until janetcode --answer <id> settles it.
type InboxApprover struct {
	SessionPath string
	SessionID   string
	Cwd         string
	// Status is the run's status as the rest of the run keeps it; the
	// approver changes only State and Ask.
	Status *session.BgStatus
	// Notify is the shell command from JANETCODE_NOTIFY, or "".
	Notify string
	// Log receives what the approver did, for the run's .bg.log.
	Log  io.Writer
	Poll time.Duration

	mu sync.Mutex
	n  int
	// stMu guards Status: the approver, the question asker (BgAsker) and
	// the end of the run all change it, and a question left open after
	// its turn went on is answered from a goroutine of its own.
	stMu sync.Mutex
}

// Update changes the run's status under the lock and writes it.
func (a *InboxApprover) Update(change func(st *session.BgStatus)) error {
	a.stMu.Lock()
	defer a.stMu.Unlock()
	change(a.Status)
	return session.WriteBgStatus(a.SessionPath, *a.Status)
}

func (a *InboxApprover) Approve(ctx context.Context, call agent.ToolCall) (agent.Decision, error) {
	d, _, err := a.ApproveWithReason(ctx, call)
	return d, err
}

// ApproveWithReason waits for the answer. A cancelled run denies.
func (a *InboxApprover) ApproveWithReason(ctx context.Context, call agent.ToolCall) (agent.Decision, string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.n++
	ask := &session.BgAsk{
		ID:      fmt.Sprintf("a%d-%d", a.n, time.Now().UnixNano()%1e6),
		Tool:    call.Name,
		Summary: present.ToolSummary(call.Name, string(call.Input), 160, a.Cwd),
		Warning: call.Warning,
		Preview: textfmt.Clip(call.Preview, 4000),
		Asked:   time.Now(),
	}
	if err := a.Update(func(st *session.BgStatus) {
		st.State, st.Ask = session.BgWaiting, ask
		// The call that needs a decision is also the run's pending
		// question, so it is what the dashboard shows first.
		st.SetPending(session.BgPending{Text: "May it run " + ask.Tool + " " + ask.Summary + "?", Choices: session.BgDecisions, About: call.Name, Ref: ask.ID, Asked: ask.Asked})
	}); err != nil {
		// Nobody could ever answer a request that was not recorded.
		fmt.Fprintf(a.Log, "[permission] %s denied: could not record the request: %v\n", call.Name, err)
		return agent.Deny, "the request could not be recorded for an answer", nil
	}
	answer := "janetcode --answer " + a.SessionID + " allow"
	fmt.Fprintf(a.Log, "[permission] waiting: %s %s — answer with: %s (or session, deny)\n", call.Name, ask.Summary, strings.TrimSuffix(answer, " allow"))
	a.notify(ctx, ask.Tool+" "+ask.Summary, answer)

	poll := a.Poll
	if poll <= 0 {
		poll = 500 * time.Millisecond
	}
	t := time.NewTicker(poll)
	defer t.Stop()
	for {
		if d, ok := session.TakeBgAnswer(a.SessionPath, ask.ID); ok {
			_ = a.Update(func(st *session.BgStatus) { st.State, st.Ask = session.BgRunning, nil; st.ClearPending(ask.ID) })
			fmt.Fprintf(a.Log, "[permission] %s: %s\n", call.Name, d)
			switch d {
			case "allow":
				return agent.Allow, "", nil
			case "session":
				return agent.AllowSession, "", nil
			default:
				return agent.Deny, "", nil
			}
		}
		select {
		case <-ctx.Done():
			_ = a.Update(func(st *session.BgStatus) { st.State, st.Ask = session.BgRunning, nil })
			return agent.Deny, agent.CancelledReason, nil
		case <-t.C:
		}
	}
}

// notify runs the JANETCODE_NOTIFY command, bounded so a hung notifier
// cannot hold the run, and says in the log when it failed.
func (a *InboxApprover) notify(ctx context.Context, what, answer string) {
	if strings.TrimSpace(a.Notify) == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := shell.Command(ctx, a.Notify)
	// The bound covers what the notifier started too: a child holding the
	// output pipe (a notifier that backgrounds its request) otherwise kept
	// CombinedOutput, and the run with it, waiting past the timeout.
	shell.Supervise(cmd)
	cmd.Dir = a.Cwd
	// The notifier is not given the provider keys: it only needs to say
	// that an answer is wanted.
	cmd.Env = append(shell.ScrubEnv(os.Environ()),
		"JANETCODE_SESSION="+a.SessionID,
		"JANETCODE_ASK="+what,
		"JANETCODE_ANSWER="+answer,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		fmt.Fprintf(a.Log, "[notify] %s failed: %v %s\n", NotifyEnv, err, textfmt.Clip(textfmt.OneLine(string(out)), 200))
	}
}
