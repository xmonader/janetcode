package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/diff"
	"github.com/xmonader/janetcode/internal/shell"
)

// `sed -i` is an edit. It rewrites a file in place, it is not reversible
// without a backup, and a script that looks obvious is routinely wrong
// about how many lines it touches. edit_file shows a diff before it runs
// and sed does not, so the shell was the way to change a file without
// anyone seeing the change first.
//
// Preview closes that: a bash call containing sed -i is run once without
// -i, and what it would do is shown as a diff in the permission prompt,
// exactly as an edit's is.
func (t Bash) Preview(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(input, &in) != nil {
		return "", nil
	}
	var out []string
	for _, seg := range shell.Segments(in.Command) {
		out = append(out, sedInPlaceDiff(ctx, t.Root, seg)...)
	}
	return strings.Join(out, "\n"), nil
}

// sedInPlaceDiff renders what one `sed -i` segment would do, per file.
func sedInPlaceDiff(ctx context.Context, root, seg string) []string {
	toks := shell.Words(seg)
	if leadingCommand(toks, 0) != "sed" {
		return nil
	}
	// Peel any wrapper so the tokens start at sed itself.
	for len(toks) > 0 && filepath.Base(toks[0]) != "sed" {
		toks = toks[1:]
	}
	if len(toks) == 0 {
		return nil
	}
	args, files, inPlace := splitSedArgs(toks[1:])
	if !inPlace || len(files) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var out []string
	for _, f := range files {
		p, err := resolvePath(root, f)
		if err != nil {
			continue
		}
		before, err := os.ReadFile(p)
		if err != nil || isBinary(before) {
			continue
		}
		cmd := exec.CommandContext(ctx, "sed", append(append([]string(nil), args...), p)...)
		cmd.Dir = root
		var buf bytes.Buffer
		cmd.Stdout, cmd.Stderr = &buf, nil
		if err := cmd.Run(); err != nil {
			out = append(out, fmt.Sprintf("(cannot preview sed -i on %s: %v)", f, err))
			continue
		}
		beforeText, _ := decodeText(before)
		afterText, _ := decodeText(buf.Bytes())
		d := diff.Unified(displayPath(root, f), beforeText, afterText, 3)
		if strings.TrimSpace(d) == "" {
			out = append(out, fmt.Sprintf("sed -i on %s changes nothing", displayPath(root, f)))
			continue
		}
		out = append(out, "sed -i would rewrite "+displayPath(root, f)+":\n"+strings.TrimRight(d, "\n"))
	}
	return out
}

// splitSedArgs separates sed's options and script from its file operands
// and reports whether -i was given. The returned args are what to run
// instead: the same invocation without -i.
func splitSedArgs(toks []string) (args, files []string, inPlace bool) {
	sawScript := false
	for i := 0; i < len(toks); i++ {
		tk := toks[i]
		switch {
		case tk == "--":
			// Everything after this is an operand.
			for _, rest := range toks[i+1:] {
				if !sawScript {
					args, sawScript = append(args, rest), true
					continue
				}
				files = append(files, rest)
			}
			return args, files, inPlace
		case tk == "-i" || tk == "--in-place":
			inPlace = true
			// GNU sed's -i takes an optional suffix only when attached, so
			// a separate next token is not ours to eat.
		case strings.HasPrefix(tk, "-i") && !strings.HasPrefix(tk, "--"):
			inPlace = true
		case strings.HasPrefix(tk, "--in-place="):
			inPlace = true
		case tk == "-e" || tk == "-f" || tk == "--expression" || tk == "--file":
			if i+1 < len(toks) {
				args = append(args, tk, toks[i+1])
				i++
				sawScript = true
			}
		case strings.HasPrefix(tk, "-") && tk != "-":
			args = append(args, tk)
		case !sawScript:
			args, sawScript = append(args, tk), true
		default:
			files = append(files, tk)
		}
	}
	return args, files, inPlace
}
