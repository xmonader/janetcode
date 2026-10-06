package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/headless"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// stringList is a repeatable flag value.
type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error {
	v = strings.TrimSpace(v)
	if v == "" {
		return fmt.Errorf("empty path")
	}
	*s = append(*s, v)
	return nil
}

// launchBackground starts a detached child that runs the prompt against
// sess and returns immediately. The child is `janetcode --resume ID -p
// prompt` with the permission and run flags the user gave (with no
// permission flag it runs in the default mode, see bgPermissionArgs);
// attach is --resume of the same id. A call the child's mode still asks about waits for
// janetcode --answer (see headless.InboxApprover).
func launchBackground(sess *session.Session, prompt string, extra []string, perms []string) error {
	if err := sess.EnsureWritten(); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		exe = os.Args[0]
	}
	args := backgroundChildArgs(sess.Meta.ID, prompt, extra, perms)
	cmd := exec.Command(exe, args...)
	cmd.Env = append(os.Environ(), headless.BgEnv+"=1")
	if dir := sess.Meta.Cwd; dir != "" {
		cmd.Dir = dir
	}
	logPath := strings.TrimSuffix(sess.Path(), ".jsonl") + ".bg.log"
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.Stdin = nil
	cmd.SysProcAttr = bgProcAttr()
	if err := cmd.Start(); err != nil {
		log.Close()
		return fmt.Errorf("background: %w", err)
	}
	pid := cmd.Process.Pid
	_ = sess.WritePID(pid)
	// Written here as well as by the child, so --sessions shows the run
	// from the moment this returns.
	_ = session.WriteBgStatus(sess.Path(), session.BgStatus{State: session.BgRunning, PID: pid, Started: time.Now()})
	_ = cmd.Process.Release()
	_ = log.Close()
	fmt.Println(sess.Meta.ID)
	fmt.Fprintf(os.Stderr, "background session %s (pid %d)\nreopen with: janetcode --resume %s\n", sess.Meta.ID, pid, sess.Meta.ID)
	return nil
}

func backgroundChildArgs(id, prompt string, extra, perms []string) []string {
	args := append([]string{"--resume", id, "-p", prompt}, perms...)
	for _, d := range extra {
		args = append(args, "--add-dir", d)
	}
	return args
}

// bgPermissionArgs are the permission flags the user gave, for the child.
// With none, the child runs in the default mode, and what it asks about
// waits for --answer: nobody is watching a background run, so it does not
// get to run every tool unless --yolo says so.
func bgPermissionArgs(o *options) []string {
	var mode, rules []string
	switch {
	case o.yolo:
		mode = []string{"--yolo"}
	case o.readonly:
		mode = []string{"--readonly"}
	case o.autoMode:
		mode = []string{"--auto"}
	case o.autoEdits:
		mode = []string{"--auto-edits"}
	}
	if o.allow != "" {
		rules = append(rules, "--allow", o.allow)
	}
	if o.deny != "" {
		rules = append(rules, "--deny", o.deny)
	}
	if o.sandboxOn {
		rules = append(rules, "--sandbox")
	}
	return append(mode, rules...)
}

// bgRunArgs are the flags that shape the run itself, for the child. The
// child reopens the session with --resume, which takes the model and the
// provider from the configuration, not from the session: without these
// `--bg --model x` ran on the default model and `--bg --max-cost 2` ran
// unattended with no spending cap. The =value spelling keeps a value that
// starts with a dash from reading as a flag.
func bgRunArgs(o *options) []string {
	var args []string
	str := func(name, v string) {
		if v != "" {
			args = append(args, "--"+name+"="+v)
		}
	}
	str("provider", o.provider)
	str("model", o.model)
	str("effort", o.effort)
	if o.maxWallTime > 0 {
		args = append(args, "--max-wall-time="+o.maxWallTime.String())
	}
	if o.maxToolCalls > 0 {
		args = append(args, "--max-tool-calls="+strconv.Itoa(o.maxToolCalls))
	}
	if o.maxCost > 0 {
		args = append(args, "--max-cost="+strconv.FormatFloat(o.maxCost, 'g', -1, 64))
	}
	if o.maxTurns > 0 {
		args = append(args, "--max-turns="+strconv.Itoa(o.maxTurns))
	}
	if o.compactAt > 0 {
		args = append(args, "--compact-at="+strconv.Itoa(o.compactAt))
	}
	str("until", o.until)
	str("append-system-prompt", o.appendSystem)
	for _, kv := range o.sets {
		str("set", kv)
	}
	return args
}

// startBgRun, in a process --bg started, records the run as running and
// installs the approver that waits for janetcode --answer. The returned
// func records how it ended. Outside --bg both do nothing.
func startBgRun(ag *agent.Agent, sess *session.Session, cwd string) func(error) {
	if os.Getenv(headless.BgEnv) != "1" || sess == nil {
		return func(error) {}
	}
	path := sess.Path()
	st := &session.BgStatus{State: session.BgRunning, PID: os.Getpid(), Started: time.Now()}
	if prev, ok, _ := session.ReadBgStatus(path); ok && !prev.Started.IsZero() {
		st.Started = prev.Started
	}
	if err := session.WriteBgStatus(path, *st); err != nil {
		fmt.Fprintf(os.Stderr, "[warn] background status: %v\n", err)
	}
	if ag.Approver == nil {
		ag.Approver = &headless.InboxApprover{
			SessionPath: path, SessionID: sess.Meta.ID, Cwd: cwd, Status: st,
			Notify: os.Getenv(headless.NotifyEnv), Log: os.Stderr,
		}
	}
	return func(err error) {
		st.State, st.Ask, st.Ended = session.BgDone, nil, time.Now()
		st.Detail = firstLine(lastAnswer(ag.Messages))
		if err != nil {
			st.State, st.Detail = session.BgFailed, firstLine(err.Error())
		}
		if werr := session.WriteBgStatus(path, *st); werr != nil {
			fmt.Fprintf(os.Stderr, "[warn] background status: %v\n", werr)
		}
		_ = sess.ClearPID()
	}
}

// lastAnswer is the text of the last assistant message.
func lastAnswer(msgs []llm.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == llm.RoleAssistant {
			if t := strings.TrimSpace(msgs[i].Text()); t != "" {
				return t
			}
		}
	}
	return ""
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return textfmt.Clip(s, 160)
}

// answerMode shows or settles the request a background run waits on.
func answerMode(w io.Writer, dir, id, decision string) error {
	path, err := session.BgSessionPath(dir, id)
	if err != nil {
		return headless.BadInput("%v", err)
	}
	st, ok, err := session.ReadBgStatus(path)
	if err != nil {
		return err
	}
	if !ok {
		return headless.BadInput("session %s has no background run (janetcode --sessions lists them)", id)
	}
	if st.State != session.BgWaiting || st.Ask == nil {
		return headless.BadInput("session %s is not waiting for an answer: %s", id, st.Line(time.Now()))
	}
	ask := st.Ask
	if decision == "" {
		fmt.Fprintf(w, "%s wants to run %s %s\n", id, ask.Tool, ask.Summary)
		if ask.Warning != "" {
			fmt.Fprintf(w, "warning: %s\n", ask.Warning)
		}
		if ask.Preview != "" {
			fmt.Fprintf(w, "\n%s\n\n", strings.TrimRight(ask.Preview, "\n"))
		}
		asked := "just now"
		if d := time.Since(ask.Asked); d >= time.Minute {
			asked = textfmt.Age(d) + " ago"
		}
		fmt.Fprintf(w, "asked %s · answer with: janetcode --answer %s allow|session|deny\n", asked, id)
		return nil
	}
	if !slices.Contains(session.BgDecisions, decision) {
		return headless.BadInput("answer %q: say allow, session or deny", decision)
	}
	if err := session.WriteBgAnswer(path, session.BgAnswer{ID: ask.ID, Decision: decision}); err != nil {
		return err
	}
	fmt.Fprintf(w, "%s: %s %s %s\n", id, decision, ask.Tool, ask.Summary)
	return nil
}
