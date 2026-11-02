package tool

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// The idle bound of an inline check: a script handed straight to an
// interpreter (python3 - <<'EOF' … EOF, echo … | python3 -) that prints nothing for inlineIdle is stopped instead
// of holding the turn for the whole shell timeout.
//
// Ported from hero_agent (tools/shell_idle.rs). effbench 2026-10-08
// (where-we-lose 2.1): on DeepSeek lean d-cronexpr-s1 one inline check, a
// brute-force "no earlier match exists" scan, ran silent until the 120 s
// shell timeout killed it; the model then made the check cheaper. A check
// is a few asserts; one silent for 45 s is a slow loop or a script waiting
// for input, and the model can act 75 s sooner.
//
// Only the inline case is bounded, and only when the call set no timeout
// of its own: a build or a test suite may be quiet for a while, and a
// heredoc that writes a FILE (cat > x.py <<EOF) is not a check. The
// interpreter runs unbuffered (PYTHONUNBUFFERED=1) so a check that does
// print shows it is alive. Deviations from hero: `python -c` / `node -e` /
// `sh -c` are not bounded. Their stdin is free for the program, and
// janetcode answers a prompt there by handing the command to the jobs
// registry (stdinwait.go); /dev/null would defeat that.
// Off: JANETCODE_OFF=idlebound.

// inlineIdle is how long an inline check may print nothing.
var inlineIdle = 45 * time.Second

const idleBoundPoll = 500 * time.Millisecond

var inlineInterpreters = map[string]bool{
	"python": true, "python3": true, "node": true, "bun": true, "deno": true,
	"ruby": true, "perl": true, "php": true, "bash": true, "sh": true,
}

// interpreters whose script can arrive on a pipe (not bash/sh: `… | sh` is an installer).
var inlineArgInterpreters = map[string]bool{
	"python": true, "python3": true, "node": true, "ruby": true, "perl": true, "php": true,
}

var (
	pyVersioned = regexp.MustCompile(`^python3\.\d+$`)
	fdRedirect  = regexp.MustCompile(`\d*>&\d+|&>|\d*>\s*/dev/null`)
)

func interpName(w string) string {
	if i := strings.LastIndexByte(w, '/'); i >= 0 {
		w = w[i+1:]
	}
	if pyVersioned.MatchString(w) {
		return "python3"
	}
	return w
}

func cmdWords(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || strings.ContainsRune(";&|()", r)
	})
}

// isInlineCheck reports whether command feeds an inline script to an
// interpreter (not to a file the command writes).
func isInlineCheck(command string) bool {
	for _, line := range strings.Split(command, "\n") {
		if i := strings.Index(line, "<<"); i >= 0 {
			before, after := line[:i], line[i+2:]
			if strings.Contains(fdRedirect.ReplaceAllString(after, ""), ">") ||
				strings.Contains(fdRedirect.ReplaceAllString(before, ""), ">") {
				continue // writes a file: cat > x.py <<EOF, python - <<EOF > out
			}
			for _, w := range cmdWords(before) {
				if strings.Contains(w, "=") {
					continue
				}
				if inlineInterpreters[interpName(w)] {
					return true
				}
			}
			continue
		}
		// `… | python3 -`: the script is the interpreter's stdin too.
		ws := strings.Fields(line)
		for i, w := range ws {
			if inlineArgInterpreters[interpName(w)] && i > 0 && (ws[i-1] == "|" || strings.HasSuffix(ws[i-1], "|")) {
				if i+1 == len(ws) || ws[i+1] == "-" {
					return true
				}
			}
		}
	}
	return false
}

func idleMessage(command string) string {
	first := strings.SplitN(command, "\n", 2)[0]
	if r := []rune(first); len(r) > 100 {
		first = string(r[:100])
	}
	return fmt.Sprintf("[inline check stopped: it printed nothing for %ds (process group killed). `%s` — a check should finish in seconds: a slow brute-force loop or a script waiting for input looks like this. Make it cheaper (smaller ranges, no exhaustive scan) and rerun it, or pass timeout_ms if it really needs longer.]", int(inlineIdle.Seconds()), first)
}

// watchIdle kills the command (via kill) once out has written nothing for
// limit; it closes fired first. It stops when stop closes.
func watchIdle(out *swapBuffer, limit time.Duration, kill func(), fired chan<- struct{}, stop <-chan struct{}) {
	last, _ := out.progress()
	since := time.Now()
	poll := idleBoundPoll
	if limit < 4*poll {
		poll = limit / 4
	}
	t := time.NewTicker(poll)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			if n, _ := out.progress(); n != last {
				last, since = n, time.Now()
			} else if time.Since(since) >= limit {
				close(fired)
				kill()
				return
			}
		}
	}
}
