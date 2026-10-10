package speccov

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Example harvesting. The worked examples in a task text are rules with
// the answer written down; the ones whose both sides can be read without
// guessing are compiled into probes — shell commands that exit 0 when the
// code gives the stated answer and 1 when it does not — so the gate can
// run them without the model writing a check of its own.
//
// Two shapes compile:
//
//   - a Python call with a literal answer, `f(3) -> 9`, `>>> f(3)` then
//     `9`, in a workspace whose task text or public tests say what to
//     import. The call runs under those imports and the result is
//     compared with ==. An example the imports do not explain (a NameError,
//     an ImportError, a syntax the parser misread) is reported as not
//     runnable, never as a failure of the code.
//   - a shell session, `$ cmd` and the lines after it, compared with
//     stdout, or with stderr and the exit code when an annotation like
//     `(stderr, exit 1)` says so.
//
// Anything else — an answer that is prose, a placeholder, an elision — is
// left out: a probe that misreads its example would send the model after
// a rule the task does not state.

// Probe is one compiled example.
type Probe struct {
	// ItemID is the matrix item the example is.
	ItemID string
	// Shape is "python" or "cli".
	Shape string
	// Label is the example as the task wrote it, for messages.
	Label string
	// Cmd is the shell command that checks it.
	Cmd string
}

// NotRunnable is printed by a probe that could not evaluate its example,
// before it exits 3: the run says nothing about the code.
const NotRunnable = "janetcode-example: not runnable"

// Harvest compiles the spec's examples that it can. imports are the Python
// import lines to run them under (Imports finds them).
func Harvest(sp Spec, imports []string) []Probe {
	ids := map[int]string{}
	for _, it := range sp.Items {
		if it.Kind == Example {
			ids[it.Line] = it.ID
		}
	}
	var out []Probe
	for _, f := range sp.Fences {
		for j := 0; j < len(f.Lines); j++ {
			ln := strings.TrimSpace(f.Lines[j])
			id := ids[f.Line+j]
			if id == "" {
				continue
			}
			if m := promptRe.FindStringSubmatch(ln); m != nil {
				var outLines []string
				for k := j + 1; k < len(f.Lines) && !promptRe.MatchString(f.Lines[k]); k++ {
					outLines = append(outLines, f.Lines[k])
				}
				if p, ok := cliProbe(m[1], outLines); ok {
					p.ItemID = id
					out = append(out, p)
				}
				continue
			}
			if len(imports) == 0 {
				continue
			}
			lhs, rhs := "", ""
			if m := replRe.FindStringSubmatch(ln); m != nil && j+1 < len(f.Lines) {
				lhs, rhs = m[1], strings.TrimSpace(f.Lines[j+1])
			} else if m := arrowRe.FindStringSubmatch(ln); m != nil {
				lhs, rhs = m[1], m[2]
			}
			if p, ok := pythonProbe(lhs, stripComment(rhs), imports); ok {
				p.ItemID = id
				out = append(out, p)
			}
		}
	}
	return out
}

// callRe is the start of a call expression: a name, dotted or not, and an
// opening parenthesis.
var callRe = regexp.MustCompile(`^[A-Za-z_][\w.]*\(`)

// pyNumberRe is one Python numeric literal.
var pyNumberRe = regexp.MustCompile(`^-?\d+(?:\.\d+)?$`)

// pyStringRe is one Python string literal on one line.
var pyStringRe = regexp.MustCompile(`[rbu]?(?:"(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*')`)

// pythonProbe compiles `lhs -> rhs` when lhs is a call and rhs a literal.
func pythonProbe(lhs, rhs string, imports []string) (Probe, bool) {
	lhs, rhs = strings.TrimSpace(lhs), strings.TrimSpace(rhs)
	if !callRe.MatchString(lhs) || !balanced(lhs) || !pyLiteral(rhs) {
		return Probe{}, false
	}
	label := lhs + " -> " + rhs
	var b strings.Builder
	b.WriteString("python3 - <<'JANETCODE_EXAMPLE'\nimport sys\nsys.path.insert(0, '.')\ntry:\n")
	for _, imp := range imports {
		b.WriteString("    " + imp + "\n")
	}
	fmt.Fprintf(&b, "    _want = eval(%s)\n", pyQuote(rhs))
	fmt.Fprintf(&b, "    _code = compile(%s, '<example>', 'eval')\n", pyQuote(lhs))
	fmt.Fprintf(&b, "except (ImportError, SyntaxError, NameError) as _e:\n    print(%s, repr(_e))\n    sys.exit(3)\n", pyQuote(NotRunnable))
	b.WriteString("try:\n    _got = eval(_code)\nexcept NameError as _e:\n")
	fmt.Fprintf(&b, "    print(%s, repr(_e))\n    sys.exit(3)\n", pyQuote(NotRunnable))
	fmt.Fprintf(&b, "if _got != _want:\n    print('janetcode-example: ' + %s)\n    print('  want', repr(_want))\n    print('  got ', repr(_got))\n    sys.exit(1)\n", pyQuote(label))
	b.WriteString("JANETCODE_EXAMPLE")
	return Probe{Shape: "python", Label: label, Cmd: b.String()}, true
}

// pyQuote quotes s as a Python string literal. Go's quoting escapes are
// a subset of Python's.
func pyQuote(s string) string { return strconv.Quote(s) }

// stripComment drops a trailing `# ...` that is outside any string.
func stripComment(s string) string {
	rest := pyStringRe.ReplaceAllStringFunc(s, func(m string) string { return strings.Repeat("x", len(m)) })
	if i := strings.Index(rest, " #"); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

// pyLiteral reports whether s is a Python literal made of strings,
// numbers, True/False/None and the brackets, commas and colons of
// tuples, lists and dicts — nothing that would need a name to evaluate.
func pyLiteral(s string) bool {
	if s == "" {
		return false
	}
	rest := pyStringRe.ReplaceAllString(s, " ")
	if strings.ContainsAny(rest, `"'`) || !balanced(s) {
		return false
	}
	for _, tok := range strings.FieldsFunc(rest, func(r rune) bool { return strings.ContainsRune("[](){},: \t", r) }) {
		if !pyNumberRe.MatchString(tok) && tok != "True" && tok != "False" && tok != "None" {
			return false
		}
	}
	return true
}

// balanced reports whether the brackets of s, outside strings, close in
// order.
func balanced(s string) bool {
	var stack []rune
	pairs := map[rune]rune{')': '(', ']': '[', '}': '{'}
	for _, r := range pyStringRe.ReplaceAllString(s, `""`) {
		switch r {
		case '(', '[', '{':
			stack = append(stack, r)
		case ')', ']', '}':
			if len(stack) == 0 || stack[len(stack)-1] != pairs[r] {
				return false
			}
			stack = stack[:len(stack)-1]
		}
	}
	return len(stack) == 0
}

// annotRe is an output annotation: `(stderr, exit 1)`, `(exit 2)`.
var annotRe = regexp.MustCompile(`\s+\((stderr)?(?:,?\s*exit(?: code)? (\d+))?\)\s*$`)

// placeholderRe marks a command or output that stands for something
// rather than being it: <path>, [-n N], an ellipsis.
var placeholderRe = regexp.MustCompile(`<[A-Za-z_ -]+>|\[[-A-Za-z_ ]+\]|\.\.\.|…`)

// cliProbe compiles a shell example: cmd and the output lines after it.
func cliProbe(cmd string, outLines []string) (Probe, bool) {
	for len(outLines) > 0 && strings.TrimSpace(outLines[len(outLines)-1]) == "" {
		outLines = outLines[:len(outLines)-1]
	}
	if len(outLines) == 0 || placeholderRe.MatchString(cmd) || strings.Contains(cmd, "JANETCODE_") {
		return Probe{}, false
	}
	stderr, code := false, 0
	want := make([]string, 0, len(outLines))
	for _, ln := range outLines {
		if placeholderRe.MatchString(ln) || strings.Contains(ln, "JANETCODE_") {
			return Probe{}, false
		}
		if m := annotRe.FindStringSubmatch(ln); m != nil && (m[1] != "" || m[2] != "") {
			if m[1] != "" {
				stderr = true
			}
			if m[2] != "" {
				code, _ = strconv.Atoi(m[2])
			}
			ln = ln[:len(ln)-len(m[0])]
		}
		want = append(want, strings.TrimRight(ln, " \t"))
	}
	label := "$ " + cmd
	redir := "2>/dev/null"
	if stderr {
		redir = "2>&1 >/dev/null"
	}
	var b strings.Builder
	// python is python3 where only python3 is installed, as task texts
	// write `python -m pkg`.
	b.WriteString("command -v python >/dev/null 2>&1 || python() { python3 \"$@\"; }\n")
	b.WriteString("_want=$(cat <<'JANETCODE_WANT'\n" + strings.Join(want, "\n") + "\nJANETCODE_WANT\n)\n")
	fmt.Fprintf(&b, "_got=$( %s %s ); _code=$?\n", cmd, redir)
	fmt.Fprintf(&b, "if [ $_code -eq 127 ]; then echo %s: command not found; exit 3; fi\n", shellQuote(NotRunnable))
	fmt.Fprintf(&b, "if [ \"$_got\" != \"$_want\" ] || [ $_code -ne %d ]; then\n", code)
	fmt.Fprintf(&b, "  printf 'janetcode-example: %%s\\nwant (exit %d):\\n%%s\\ngot (exit %%s):\\n%%s\\n' %s \"$_want\" \"$_code\" \"$_got\"\n  exit 1\nfi", code, shellQuote(label))
	return Probe{Shape: "cli", Label: label, Cmd: b.String()}, true
}

// shellQuote quotes s for a POSIX shell.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// importRe is a Python import line.
var importRe = regexp.MustCompile(`^(?:from\s+[\w.]+\s+import\s+[\w., ]+|import\s+[\w.]+(?:\s+as\s+\w+)?(?:\s*,\s*[\w.]+)*)\s*$`)

// Imports are the Python import lines an example runs under: those in the
// task text's code blocks, then those at the top level of the public
// Python tests, without test-only modules.
func Imports(sp Spec, tests map[string]string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(ln string) {
		ln = strings.TrimSpace(ln)
		if !importRe.MatchString(ln) || seen[ln] {
			return
		}
		for _, skip := range []string{"pytest", "unittest", "conftest", "hypothesis"} {
			if strings.Contains(ln, skip) {
				return
			}
		}
		seen[ln] = true
		out = append(out, ln)
	}
	for _, f := range sp.Fences {
		if f.Lang != "" && f.Lang != "python" && f.Lang != "py" && f.Lang != "pycon" {
			continue
		}
		for _, ln := range f.Lines {
			add(ln)
		}
	}
	paths := make([]string, 0, len(tests))
	for p := range tests {
		if strings.HasSuffix(p, ".py") {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	for _, p := range paths {
		for _, ln := range strings.Split(tests[p], "\n") {
			if ln != "" && ln[0] != ' ' && ln[0] != '\t' {
				add(ln)
			}
		}
	}
	return out
}
