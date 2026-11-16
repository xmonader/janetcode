package headless

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"sync"

	"github.com/charmbracelet/x/ansi"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/i18n"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/present"
	"github.com/xmonader/janetcode/internal/prompt"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// The screen-reader interface: a conversation on plain lines. The
// full-screen terminal interface redraws a frame many times a second — a
// spinner, a status bar, a transcript that re-wraps — and a screen reader
// reads each redraw as new text, or nothing useful at all. Here every
// line is written once, below the last, and never moved or overwritten:
// no cursor addressing, no carriage returns, no colour, no box drawing,
// no animation. Every state is said in words ("Working.", "Waiting for
// permission: …") where the full-screen interface shows it as a glyph or
// a colour. It runs the same agent through the same turn as -p does; only
// the rendering and the reading of input differ.

// ConverseOptions configures a screen-reader conversation.
type ConverseOptions struct {
	// Options is the run as -p would take it; Stdout carries everything
	// (stderr's tool activity included) and Stdin both the prompts and
	// the permission answers.
	Options Options
	// Voice, when set, records a prompt until stop is closed and returns
	// its transcript (/voice).
	Voice func(ctx context.Context, stop <-chan struct{}) (string, error)
	// Interrupts delivers ctrl+c. Nil installs a handler for os.Interrupt.
	// The first cancels a running turn; at the prompt it exits.
	Interrupts <-chan os.Signal
}

// errQuit ends the conversation cleanly.
var errQuit = errors.New("quit")

// ScreenReaderWanted reports whether the environment asks for the
// screen-reader interface: JANETCODE_SCREEN_READER=1, or
// ACCESSIBILITY_ENABLED=1, which desktops that start a screen reader at
// login export (GNOME's Orca setup among them). JANETCODE_SCREEN_READER=0
// turns the detection off.
func ScreenReaderWanted(getenv func(string) string) (want, forcedOff bool) {
	switch strings.ToLower(strings.TrimSpace(getenv("JANETCODE_SCREEN_READER"))) {
	case "1", "true", "yes", "on":
		return true, false
	case "0", "false", "no", "off":
		return false, true
	}
	return getenv("ACCESSIBILITY_ENABLED") == "1", false
}

// Converse runs the screen-reader conversation until /quit, end of input
// or ctrl+c at the prompt.
func Converse(ctx context.Context, co ConverseOptions) error {
	opts := co.Options
	ag := opts.Agent
	// One stream, in order: a reader cannot follow two interleaved ones.
	out := io.Writer(plainWriter{opts.Stdout})
	opts.Stdout, opts.Stderr = out, out
	sa := newStdinApprover(opts.Stdin, out)
	sa.screenReader = true
	defer sa.Close()
	if ag.Approver == nil {
		ag.Approver = sa
		defer func() { ag.Approver = nil }()
	}
	p := &spoken{out: out}
	opts.printer = p.event
	opts.conversing = true
	rec := newRecorder(opts, FormatText)

	intr := co.Interrupts
	if intr == nil {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, os.Interrupt)
		defer signal.Stop(ch)
		intr = ch
	}
	var mu sync.Mutex
	var cancelTurn context.CancelFunc
	ctx, quit := context.WithCancel(ctx)
	defer quit()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-intr:
				mu.Lock()
				c := cancelTurn
				mu.Unlock()
				if c != nil {
					c()
				} else {
					quit()
				}
			}
		}
	}()

	cwd := opts.Cwd
	fmt.Fprintln(out, i18n.Tf("janetcode, screen reader mode. Working in %s. Model %s.", cwd, ag.Settings().Model))
	fmt.Fprintln(out, i18n.T("Type a request and press enter. /help lists commands, /quit exits."))
	mutated := false
	defer func() { endSession(context.WithoutCancel(ctx), opts, mutated) }()
	for {
		fmt.Fprintln(out, i18n.T("Ready."))
		fmt.Fprint(out, "> ")
		line, err := sa.nextLine(ctx)
		if err != nil {
			fmt.Fprintln(out)
			fmt.Fprintln(out, i18n.T("Goodbye."))
			return nil
		}
		text, err := converseCommand(ctx, co, sa, rec, out, strings.TrimSpace(line))
		if errors.Is(err, errQuit) {
			fmt.Fprintln(out, i18n.T("Goodbye."))
			return nil
		}
		if text == "" {
			continue
		}
		expanded, atts := prompt.ExpandMentions(cwd, text)
		blocks := append([]llm.Block{{Type: llm.BlockText, Text: expanded}}, prompt.ImageBlocks(atts)...)
		tctx, cancel := context.WithCancel(ctx)
		mu.Lock()
		cancelTurn = cancel
		mu.Unlock()
		fmt.Fprintln(out, i18n.T("Working."))
		p.reset()
		err = turn(tctx, opts, rec, blocks)
		p.flush()
		mu.Lock()
		cancelTurn = nil
		mu.Unlock()
		cancel()
		if err == nil && ag.LastTurnMutated() {
			mutated = true
		}
		switch {
		case err == nil:
			fmt.Fprintln(out, i18n.T("Turn finished."))
		case errors.Is(err, context.Canceled) && ctx.Err() == nil:
			fmt.Fprintln(out, i18n.T("Turn cancelled."))
		case ctx.Err() != nil:
			fmt.Fprintln(out, i18n.T("Goodbye."))
			return nil
		default:
			fmt.Fprintln(out, i18n.Tf("Turn failed: %s", safeLine(err.Error(), 400)))
		}
	}
}

// converseCommand handles the few commands the line interface has and
// returns the prompt to send, if any.
func converseCommand(ctx context.Context, co ConverseOptions, sa *stdinApprover, rec *recorder, out io.Writer, line string) (string, error) {
	switch strings.ToLower(line) {
	case "":
		return "", nil
	case "/quit", "/exit", "/q":
		return "", errQuit
	case "/help":
		fmt.Fprintln(out, i18n.T("Commands: /help, /quit, /cost, /voice. Everything else you type is sent to the model."))
		fmt.Fprintln(out, i18n.T("permission prompt: y allow once · a allow this tool for the session · r save a rule so matching calls never ask again · n deny (then type why, or enter to skip) · esc deny · c cancel the whole turn"))
		return "", nil
	case "/cost", "/usage":
		if spent, ok := rec.spentUSD(); ok {
			fmt.Fprintln(out, i18n.Tf("Cost so far: %s.", llm.FormatUSDPrecise(spent)))
		} else {
			fmt.Fprintln(out, i18n.Tf("Cost so far: %s.", "unpriced"))
		}
		return "", nil
	case "/voice":
		if co.Voice == nil {
			fmt.Fprintln(out, i18n.Tf("no recording: %s", "voice is not available here"))
			return "", nil
		}
		// The recording is stopped by a line, read by the one reader
		// the prompts and permission answers share.
		stop := make(chan struct{})
		type result struct {
			text string
			err  error
		}
		got := make(chan result, 1)
		// A recording that ends by itself (no recorder, no device) stops
		// the wait for the line: an abandoned wait takes nothing, so the
		// next line typed is the next prompt.
		lctx, lcancel := context.WithCancel(ctx)
		defer lcancel()
		go func() {
			t, err := co.Voice(ctx, stop)
			got <- result{t, err}
			lcancel()
		}()
		fmt.Fprintln(out, i18n.T("Recording. Press enter to stop."))
		_, lerr := sa.nextLine(lctx)
		close(stop)
		if lerr == nil {
			fmt.Fprintln(out, i18n.T("Transcribing…"))
		}
		r := <-got
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if lerr != nil && lctx.Err() == nil {
			return "", lerr // input closed
		}
		heard, err := r.text, r.err
		if err != nil {
			fmt.Fprintln(out, i18n.Tf("no recording: %s", safeLine(err.Error(), 300)))
			return "", nil
		}
		if strings.TrimSpace(heard) == "" {
			fmt.Fprintln(out, i18n.T("nothing was heard"))
			return "", nil
		}
		fmt.Fprintln(out, i18n.Tf("Heard: %s", safe(heard)))
		fmt.Fprintln(out, i18n.T("Press enter to send it, or type a correction."))
		fmt.Fprint(out, "> ")
		fix, err := sa.nextLine(ctx)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(fix) != "" {
			return strings.TrimSpace(fix), nil
		}
		return heard, nil
	}
	return line, nil
}

// spoken renders a turn's events as labelled lines for a screen reader.
// The model's streamed text is held until a line is complete, so a
// reader is handed whole lines rather than a word at a time.
type spoken struct {
	out      io.Writer
	pending  strings.Builder
	answered bool
	thinking bool
	inCode   bool
}

func (p *spoken) reset() { p.pending.Reset(); p.answered, p.thinking, p.inCode = false, false, false }

func (p *spoken) line(s string) { fmt.Fprintln(p.out, s) }

// flush writes the held partial line, if any.
func (p *spoken) flush() {
	if p.pending.Len() == 0 {
		return
	}
	p.say(p.pending.String())
	p.pending.Reset()
}

func (p *spoken) say(s string) {
	if strings.HasPrefix(strings.TrimSpace(s), "```") {
		lang := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "```"))
		if p.inCode {
			p.line("End of code.")
		} else if lang != "" {
			p.line("Code, " + lang + ":")
		} else {
			p.line("Code:")
		}
		p.inCode = !p.inCode
		return
	}
	if p.inCode {
		p.line(s)
		return
	}
	p.line(readable(s))
}

func (p *spoken) event(e agent.Event) {
	switch e.Kind {
	case agent.EvThinking:
		if !p.thinking && !p.answered {
			p.line(i18n.T("Thinking."))
			p.thinking = true
		}
	case agent.EvText:
		if !p.answered {
			p.line(i18n.T("Answer:"))
			p.answered = true
		}
		p.pending.WriteString(safe(e.Text))
		for {
			s := p.pending.String()
			i := strings.IndexByte(s, '\n')
			if i < 0 {
				break
			}
			p.pending.Reset()
			p.pending.WriteString(s[i+1:])
			if strings.TrimSpace(s[:i]) != "" {
				p.say(s[:i])
			}
		}
	case agent.EvTextBlockDone:
		p.flush()
		p.answered, p.thinking = false, false
	case agent.EvToolRun:
		p.flush()
		p.answered, p.thinking = false, false
		what := safe(e.Call.Name)
		if e.Call.Intent != "" {
			what += ", " + safe(e.Call.Intent)
		} else if in := safeLine(string(e.Call.Input), 160); in != "" {
			what += " " + in
		}
		p.line(i18n.Tf("Running tool: %s.", what))
	case agent.EvToolResult:
		p.flush()
		name := ""
		if e.Call != nil {
			name = safe(e.Call.Name)
		}
		if e.IsError {
			p.line(i18n.Tf("Tool %s failed.", name))
		} else {
			p.line(i18n.Tf("Tool %s finished.", name))
		}
		if out := safeLine(e.Output, 300); out != "" {
			p.line(i18n.Tf("Result: %s", out))
		}
	case agent.EvVerdict:
		p.flush()
		p.line(i18n.Tf("Outcome: %s", safeLine(e.Text, 300)))
		if e.Evidence != nil {
			if line := e.Evidence.Line(); line != "" {
				p.line(i18n.Tf("Evidence: %s", safeLine(line, 300)))
			}
		}
	case agent.EvRetry:
		p.line(i18n.Tf("Retrying: %s", safeLine(e.Text, 200)))
	case agent.EvDiscard:
		p.pending.Reset()
		p.line(i18n.Tf("Notice: %s", "the answer so far was cut off and is being sent again; ignore it"))
		p.answered = false
	case agent.EvNotice:
		if !present.Routine(e.Text) {
			p.flush()
			p.line(i18n.Tf("Notice: %s", safeLine(e.Text, 400)))
		}
	case agent.EvCompacted:
		p.line(i18n.Tf("Notice: %s", safeLine(e.Text, 300)))
	case agent.EvTurnDone:
		// The running cost is /cost's: a figure per call is noise when
		// it is read aloud.
		p.flush()
	}
}

// plainWriter is the line interface's output: whatever reaches it — the
// model's text, a tool's output, a diff — loses every escape sequence
// (colour included) and every carriage return, the two ways a line can
// repaint one already written.
type plainWriter struct{ w io.Writer }

func (p plainWriter) Write(b []byte) (int, error) {
	s := ansi.Strip(textfmt.StripUnsafeANSI(string(b)))
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	// A bell rang the reader's terminal and a backspace ate the character
	// before it: neither is text to read.
	s = strings.Map(func(r rune) rune {
		if (r < 0x20 && r != '\n' && r != '\t') || r == 0x7f {
			return -1
		}
		return r
	}, s)
	if _, err := io.WriteString(p.w, s); err != nil {
		return 0, err
	}
	return len(b), nil
}

var (
	mdHeading = regexp.MustCompile(`^\s{0,3}#{1,6}\s+`)
	mdEmph    = regexp.MustCompile(`(\*\*|__)(.+?)(\*\*|__)`)
	mdCode    = regexp.MustCompile("`([^`]+)`")
	mdRule    = regexp.MustCompile(`^\s*([-*_])(\s*[-*_]){2,}\s*$`)
	mdBoxRun  = regexp.MustCompile(`[─━│┃┌┐└┘├┤┬┴┼═║╔╗╚╝╠╣╦╩╬]{2,}`)
)

// readable takes the markup a screen reader would read out symbol by
// symbol off a line of the model's markdown: heading hashes, emphasis
// stars, inline-code backticks, horizontal rules and runs of box-drawing
// characters. Bullets and numbers stay: they are how a list is heard.
func readable(s string) string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if mdRule.MatchString(l) {
			continue
		}
		l = mdHeading.ReplaceAllString(l, "")
		l = mdEmph.ReplaceAllString(l, "$2")
		l = mdCode.ReplaceAllString(l, "$1")
		l = mdBoxRun.ReplaceAllString(l, " ")
		l = textfmt.StripUnsafeANSI(l)
		out = append(out, strings.TrimRight(l, " "))
	}
	return strings.Join(out, "\n")
}
