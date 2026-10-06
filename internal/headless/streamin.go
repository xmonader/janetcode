package headless

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/xmonader/janetcode/internal/llm"
)

// --input-format stream-json keeps one headless process for a whole
// conversation: a program writes one JSON object per line on stdin, each
// user message is a turn, and each turn ends with its own result line on
// stdout. The program can interrupt the running turn without ending the
// session, which a -p run per message cannot offer: every one of those
// pays the start-up, the MCP connections and the prompt cache again.
//
// Accepted lines:
//
//	{"type":"user","text":"..."}
//	{"type":"user","message":{"role":"user","content":"..." | [{"type":"text","text":"..."}]}}
//	{"type":"control","subtype":"interrupt"}
//	{"type":"control_request","request_id":"r1","request":{"subtype":"interrupt"}}
//
// The second and fourth are the shapes Claude Code's stream-json input
// uses, so a driver written for it works unchanged.

// InputFormat is how a headless run reads its prompts.
type InputFormat string

const (
	// InputText is the default: one prompt, from -p (and piped stdin).
	InputText InputFormat = "text"
	// InputStreamJSON reads user messages and controls from stdin, one
	// JSON object per line, until EOF.
	InputStreamJSON InputFormat = "stream-json"
)

// ParseInputFormat validates the --input-format value.
func ParseInputFormat(s string) (InputFormat, error) {
	switch InputFormat(strings.TrimSpace(s)) {
	case "", InputText:
		return InputText, nil
	case InputStreamJSON:
		return InputStreamJSON, nil
	}
	return "", fmt.Errorf("unknown --input-format %q: want text or stream-json", s)
}

// maxInputLine bounds one stdin line; a message larger than this is
// reported and skipped rather than held in memory without limit.
const maxInputLine = 16 << 20

type inputLine struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	Text    string `json:"text"`
	Message *struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
	RequestID string `json:"request_id"`
	Request   *struct {
		Subtype string `json:"subtype"`
	} `json:"request"`
}

// inputMsg is one parsed line: a prompt, or an interrupt.
type inputMsg struct {
	text      string
	interrupt bool
	requestID string
}

// parseInputLine reads one stdin line. An empty line is nothing at all.
func parseInputLine(b []byte) (inputMsg, bool, error) {
	if len(strings.TrimSpace(string(b))) == 0 {
		return inputMsg{}, false, nil
	}
	var l inputLine
	if err := json.Unmarshal(b, &l); err != nil {
		return inputMsg{}, false, fmt.Errorf("not a JSON object: %v", err)
	}
	switch l.Type {
	case "user":
		text := l.Text
		if l.Message != nil {
			t, err := contentText(l.Message.Content)
			if err != nil {
				return inputMsg{}, false, err
			}
			text = t
		}
		if strings.TrimSpace(text) == "" {
			return inputMsg{}, false, errors.New("user message has no text")
		}
		return inputMsg{text: text}, true, nil
	case "control":
		if l.Subtype == "interrupt" {
			return inputMsg{interrupt: true}, true, nil
		}
		return inputMsg{}, false, fmt.Errorf("unknown control %q: want interrupt", l.Subtype)
	case "control_request":
		if l.Request != nil && l.Request.Subtype == "interrupt" {
			return inputMsg{interrupt: true, requestID: l.RequestID}, true, nil
		}
		return inputMsg{}, false, errors.New("unsupported control_request: only interrupt is")
	}
	return inputMsg{}, false, fmt.Errorf("unknown message type %q: want user or control", l.Type)
}

// contentText flattens a message's content, a string or a list of
// blocks, to its text. Non-text blocks are refused rather than dropped:
// a prompt silently missing its image would be answered as if it had
// none.
func contentText(raw json.RawMessage) (string, error) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, nil
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return "", errors.New("message.content must be a string or a list of text blocks")
	}
	var sb strings.Builder
	for _, b := range blocks {
		if b.Type != "text" {
			return "", fmt.Errorf("message.content block type %q is not supported: only text is", b.Type)
		}
		if sb.Len() > 0 {
			sb.WriteString("\n\n")
		}
		sb.WriteString(b.Text)
	}
	return sb.String(), nil
}

// readInput turns stdin into messages until EOF or ctx ends. Prompts go
// to prompts, in order; an interrupt calls interrupt at once, even while
// a turn is running, which is the point of reading on a goroutine of its
// own. A bad line is reported on the stream and skipped.
func readInput(ctx context.Context, in io.Reader, rec *recorder, prompts chan<- inputMsg, interrupt func(inputMsg)) {
	defer close(prompts)
	br := bufio.NewReaderSize(in, 64<<10)
	for n := 1; ; n++ {
		line, err := readLine(br)
		if len(line) > 0 || errors.Is(err, errLineTooLong) {
			msg, ok, perr := parseInputLine(line)
			switch {
			case errors.Is(err, errLineTooLong):
				rec.emitLocked(streamEvent{Type: "input_error", Text: fmt.Sprintf("line %d: longer than %d bytes; skipped", n, maxInputLine)})
			case perr != nil:
				rec.emitLocked(streamEvent{Type: "input_error", Text: fmt.Sprintf("line %d: %v; skipped", n, perr)})
			case ok && msg.interrupt:
				interrupt(msg)
			case ok:
				select {
				case prompts <- msg:
				case <-ctx.Done():
					return
				}
			}
		}
		if err != nil && !errors.Is(err, errLineTooLong) {
			return // EOF ends the conversation; any other error does too
		}
	}
}

var errLineTooLong = errors.New("line too long")

// readLine reads up to a newline, discarding the rest of a line past
// maxInputLine.
func readLine(br *bufio.Reader) ([]byte, error) {
	var buf []byte
	for {
		chunk, err := br.ReadSlice('\n')
		if len(buf)+len(chunk) > maxInputLine {
			for errors.Is(err, bufio.ErrBufferFull) {
				_, err = br.ReadSlice('\n')
			}
			if err != nil && !errors.Is(err, io.EOF) {
				return nil, err
			}
			return nil, errLineTooLong
		}
		buf = append(buf, chunk...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return buf, err
	}
}

// runStream is Run for --input-format stream-json.
func runStream(ctx context.Context, opts Options, rec *recorder) error {
	ag := opts.Agent
	if ag.Approver == nil {
		// Stdin carries the conversation, so nothing can be asked on it:
		// the approver denies whatever the rules and mode do not allow.
		sa := newStdinApprover(nil, opts.Stderr)
		defer func() {
			_ = sa.Close()
			ag.Approver = nil
		}()
		ag.Approver = sa
	}
	ag.MaxToolCalls = opts.MaxToolCalls
	if opts.MaxCostUSD > 0 {
		ag.MaxCostUSD = opts.MaxCostUSD
	}
	var (
		mu         sync.Mutex
		cancelTurn context.CancelFunc
		interrupts int // interrupts that landed on the running turn
	)
	interrupt := func(m inputMsg) {
		mu.Lock()
		running := cancelTurn != nil
		if running {
			interrupts++
			cancelTurn()
		}
		mu.Unlock()
		ack := "no turn was running"
		if running {
			ack = "interrupted the running turn"
		}
		rec.emitLocked(streamEvent{Type: "control_response", ID: m.requestID, Text: ack})
	}
	prompts := make(chan inputMsg, 64)
	readCtx, stopReading := context.WithCancel(ctx)
	defer stopReading()
	go readInput(readCtx, opts.Input, rec, prompts, interrupt)

	first := opts.Prompt
	var last error
	handled := false
	for {
		var text string
		if first != "" {
			text, first = first, ""
		} else {
			select {
			case m, ok := <-prompts:
				if !ok {
					if !handled {
						return nil // empty input: Run writes the one result
					}
					rec.perTurn = true
					return last
				}
				text = m.text
			case <-ctx.Done():
				rec.perTurn = handled
				return ctx.Err()
			}
		}
		handled = true
		turnCtx, cancel := context.WithCancel(ctx)
		mu.Lock()
		cancelTurn, interrupts = cancel, 0
		mu.Unlock()
		rec.beginTurn()
		err := turn(turnCtx, opts, rec, []llm.Block{{Type: llm.BlockText, Text: text}})
		mu.Lock()
		cancelTurn = nil
		wasInterrupted := interrupts > 0
		mu.Unlock()
		cancel()
		if err != nil && ctx.Err() != nil {
			// The process is being stopped, not the turn: the last result
			// is Run's to write.
			return ctx.Err()
		}
		if err != nil && wasInterrupted {
			err = fmt.Errorf("turn interrupted: %w", context.Canceled)
		}
		last = err
		rec.writeResult(ag, err)
		if errors.Is(err, ErrBudget) || errors.Is(err, ErrToolLimit) {
			// Every later message would be refused on its first call.
			rec.perTurn = true
			return err
		}
	}
}
