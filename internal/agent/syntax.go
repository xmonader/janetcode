package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/textfmt"
)

// Per-edit feedback that costs no model call: a syntax check with the
// language's own tool when it is installed, and a pointer to the test file
// that covers the edited file. Both are appended to the edit result so the
// model sees them before deciding what to do next.

const syntaxTimeout = 10 * time.Second

// syntaxCheckers maps an extension to the command that parses one file.
// Each must exit non-zero on a syntax error and print the reason.
var syntaxCheckers = map[string][]string{
	".go": {"gofmt", "-e", "-l"},
	// compile() rather than -m py_compile, which also writes the
	// compiled file into a __pycache__ beside the source: a check must
	// not leave anything in the user's tree.
	".py":   {"python3", "-c", `import sys; compile(open(sys.argv[1], "rb").read(), sys.argv[1], "exec")`},
	".js":   {"node", "--check"},
	".mjs":  {"node", "--check"},
	".cjs":  {"node", "--check"},
	".rb":   {"ruby", "-c"},
	".sh":   {"bash", "-n"},
	".json": {"python3", "-c", `import json,sys; json.load(open(sys.argv[1]))`},
}

// syntaxFeedback checks abs and returns a note for the model, or "".
func syntaxFeedback(ctx context.Context, abs string) string {
	argv, ok := syntaxCheckers[strings.ToLower(filepath.Ext(abs))]
	if !ok {
		return ""
	}
	if _, err := exec.LookPath(argv[0]); err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, syntaxTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], append(argv[1:], abs)...)
	cmd.Dir = filepath.Dir(abs)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return ""
	}
	msg := strings.TrimSpace(string(out))
	if msg == "" {
		msg = err.Error()
	}
	return fmt.Sprintf("Syntax check (%s) failed on %s:\n%s", argv[0], filepath.Base(abs), textfmt.TruncateTo(msg, 1500))
}

// coveringTest names the test file that most likely covers abs, if one
// exists next to it: Go's _test.go twin, Python's test_ prefix, and
// JavaScript/TypeScript .test/.spec twins.
func coveringTest(abs string) string {
	dir, base := filepath.Split(abs)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	if strings.HasSuffix(stem, "_test") || strings.HasPrefix(stem, "test_") || strings.HasSuffix(stem, ".test") || strings.HasSuffix(stem, ".spec") {
		return ""
	}
	var candidates []string
	switch ext {
	case ".go":
		candidates = []string{stem + "_test.go"}
	case ".py":
		candidates = []string{"test_" + base, filepath.Join("tests", "test_"+base), filepath.Join("..", "tests", "test_"+base)}
	case ".js", ".ts", ".jsx", ".tsx", ".mjs":
		candidates = []string{stem + ".test" + ext, stem + ".spec" + ext, filepath.Join("__tests__", base)}
	case ".rs":
		return "" // Rust tests live in the same file
	default:
		return ""
	}
	for _, c := range candidates {
		p := filepath.Join(dir, c)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

// editFeedback builds the note appended to a successful write or edit.
func editFeedback(ctx context.Context, abs string, testHint bool) string {
	if abs == "" {
		return ""
	}
	var notes []string
	if s := syntaxFeedback(ctx, abs); s != "" {
		notes = append(notes, s)
	}
	if t := coveringTest(abs); testHint && t != "" {
		rel := t
		if r, err := filepath.Rel(filepath.Dir(abs), t); err == nil {
			rel = r // next to the edited file: "calc_test.go", "tests/test_x.py"
		}
		notes = append(notes, "Tests covering this file (same directory as the edited file): "+rel)
	}
	if len(notes) == 0 {
		return ""
	}
	return "\n\n" + strings.Join(notes, "\n")
}
