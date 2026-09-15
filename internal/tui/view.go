package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/xmonader/janetcode/internal/gitx"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// openCommand shows a file inline, centred on a line when one is given.
func (m *model) openCommand(arg string) {
	if arg == "" {
		m.transcript.errorf("usage: /open path[:line]")
		return
	}
	path, line := arg, 0
	if i := strings.LastIndexByte(arg, ':'); i > 0 {
		if n, err := strconv.Atoi(arg[i+1:]); err == nil {
			path, line = arg[:i], n
		}
	}
	abs := path
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(m.opts.Cwd, path)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		m.transcript.errorf("open: %v", err)
		return
	}
	if bytes.IndexByte(data, 0) >= 0 {
		m.transcript.errorf("%s is binary (%s)", path, textfmt.Bytes(int64(len(data))))
		return
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	const window = 60
	from, to := 1, len(lines)
	if line > 0 {
		if line > len(lines) {
			m.transcript.errorf("%s has %d lines", path, len(lines))
			return
		}
		from, to = line-window/2, line+window/2
	} else if len(lines) > window*2 {
		to = window * 2
	}
	if from < 1 {
		from = 1
	}
	if to > len(lines) {
		to = len(lines)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "# %s  lines %d-%d of %d\n", path, from, to, len(lines))
	w := len(strconv.Itoa(to))
	for n := from; n <= to; n++ {
		mark := " "
		if n == line {
			mark = "▶"
		}
		fmt.Fprintf(&sb, "%s%*d │ %s\n", mark, w, n, strings.ReplaceAll(lines[n-1], "\t", "    "))
	}
	if to < len(lines) {
		fmt.Fprintf(&sb, "  … %d more lines; /open %s:%d continues\n", len(lines)-to, path, to+1)
	}
	m.transcript.code(sb.String())
}

// changedFile is one entry of git status.
type changedFile struct {
	Path      string
	Untracked bool
	Deleted   bool
}

// diffCommand lists changed files, shows one file's hunks, or reverts a hunk.
func (m *model) diffCommand(args []string) {
	if _, err := gitOut(m.opts.Cwd, "rev-parse", "--show-toplevel"); err != nil {
		m.transcript.errorf("/diff needs a git repository")
		return
	}
	if len(args) >= 2 && args[0] == "revert" {
		m.revertHunk(args[1])
		return
	}
	if len(args) == 1 {
		m.showFileDiff(args[0])
		return
	}
	files, err := changedFiles(m.opts.Cwd)
	if err != nil {
		m.transcript.errorf("git status: %v", err)
		return
	}
	if len(files) == 0 {
		m.note("working tree clean")
		return
	}
	var sb strings.Builder
	sb.WriteString("# changes against HEAD\n")
	for _, f := range files {
		switch {
		case f.Untracked:
			n := 0
			if data, err := os.ReadFile(filepath.Join(m.opts.Cwd, f.Path)); err == nil {
				n = strings.Count(string(data), "\n")
			}
			fmt.Fprintf(&sb, "+ %s  (new, %d lines)\n", f.Path, n)
		case f.Deleted:
			fmt.Fprintf(&sb, "- %s  (deleted)\n", f.Path)
		default:
			hunks, add, del := hunkStats(m.opts.Cwd, f.Path)
			fmt.Fprintf(&sb, "  %s  (%d hunks, +%d -%d)\n", f.Path, hunks, add, del)
		}
	}
	sb.WriteString("/diff <path> shows the hunks · /diff revert <path>:<n> reverts one · /undo reverts a whole turn")
	m.transcript.code(sb.String())
}

func (m *model) showFileDiff(path string) {
	patch, err := gitOut(m.opts.Cwd, "diff", "--no-color", "--", path)
	if err != nil {
		m.transcript.errorf("git diff: %v", err)
		return
	}
	if strings.TrimSpace(patch) == "" {
		if _, err := os.Stat(filepath.Join(m.opts.Cwd, path)); err == nil {
			if out, _ := gitOut(m.opts.Cwd, "ls-files", "--error-unmatch", "--", path); out == "" {
				m.openCommand(path)
				m.transcript.info(path + " is untracked (new): shown in full; delete it yourself or /undo the turn that made it")
				return
			}
		}
		m.note(path + " is unchanged")
		return
	}
	_, hunks := splitPatch(patch)
	var sb strings.Builder
	fmt.Fprintf(&sb, "# %s  %d hunks\n", path, len(hunks))
	for i, h := range hunks {
		fmt.Fprintf(&sb, "@@ hunk %d/%d %s\n", i+1, len(hunks), strings.TrimPrefix(firstLineOf(h), "@@ "))
		body := strings.SplitN(h, "\n", 2)
		if len(body) == 2 {
			sb.WriteString(strings.TrimRight(body[1], "\n") + "\n")
		}
	}
	fmt.Fprintf(&sb, "/diff revert %s:<n> reverts one hunk", path)
	m.transcript.code(sb.String())
}

// revertHunk applies one hunk of a file's diff in reverse.
func (m *model) revertHunk(spec string) {
	i := strings.LastIndexByte(spec, ':')
	if i <= 0 {
		m.transcript.errorf("usage: /diff revert <path>:<hunk>")
		return
	}
	path := spec[:i]
	n, err := strconv.Atoi(spec[i+1:])
	if err != nil || n < 1 {
		m.transcript.errorf("usage: /diff revert <path>:<hunk>")
		return
	}
	patch, err := gitOut(m.opts.Cwd, "diff", "--no-color", "--", path)
	if err != nil || strings.TrimSpace(patch) == "" {
		m.transcript.errorf("%s has no tracked changes to revert (untracked files: delete them, or /undo)", path)
		return
	}
	header, hunks := splitPatch(patch)
	if n > len(hunks) {
		m.transcript.errorf("%s has %d hunks", path, len(hunks))
		return
	}
	single := header + hunks[n-1]
	if _, err := gitx.RunInput(m.life, m.opts.Cwd, []byte(single), "apply", "-R", "--recount", "-"); err != nil {
		m.transcript.errorf("git apply -R: %s", gitx.Detail(err))
		return
	}
	m.note(fmt.Sprintf("reverted hunk %d of %s (%d left)", n, path, len(hunks)-1))
}

// splitPatch separates a single-file git diff into its header and hunks,
// each hunk keeping its @@ line and a trailing newline.
func splitPatch(patch string) (header string, hunks []string) {
	lines := strings.SplitAfter(patch, "\n")
	var cur strings.Builder
	inHunk := false
	for _, l := range lines {
		if strings.HasPrefix(l, "@@") {
			if inHunk {
				hunks = append(hunks, cur.String())
				cur.Reset()
			}
			inHunk = true
		}
		if inHunk {
			cur.WriteString(l)
		} else {
			header += l
		}
	}
	if inHunk && cur.Len() > 0 {
		hunks = append(hunks, cur.String())
	}
	return header, hunks
}

func hunkStats(root, path string) (hunks, add, del int) {
	patch, err := gitOut(root, "diff", "--no-color", "--", path)
	if err != nil {
		return 0, 0, 0
	}
	for _, l := range strings.Split(patch, "\n") {
		switch {
		case strings.HasPrefix(l, "@@"):
			hunks++
		case strings.HasPrefix(l, "+") && !strings.HasPrefix(l, "+++"):
			add++
		case strings.HasPrefix(l, "-") && !strings.HasPrefix(l, "---"):
			del++
		}
	}
	return hunks, add, del
}

func changedFiles(root string) ([]changedFile, error) {
	out, err := gitOut(root, "status", "--porcelain", "-z", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	var files []changedFile
	for _, e := range strings.Split(out, "\x00") {
		if len(e) < 4 {
			continue
		}
		f := changedFile{Path: e[3:], Untracked: e[:2] == "??", Deleted: e[1] == 'D' || e[0] == 'D'}
		files = append(files, f)
	}
	return files, nil
}

// gitOut is git's untrimmed stdout (a patch keeps its final newline); a
// failure is reported as git's own stderr.
func gitOut(dir string, args ...string) (string, error) {
	out, err := gitx.RunBytes(context.Background(), dir, nil, args...)
	if err != nil {
		return "", errors.New(gitx.Detail(err))
	}
	return string(out), nil
}

func firstLineOf(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
