package prompt

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xmonader/janetcode/internal/textfmt"
)

// Project instructions are not one file. A repository says something
// about itself at its root, and a package three directories down often
// says something more particular; reading only the working directory
// finds the second and misses the first, or the other way round
// depending on where the session was started. Every instruction file
// from the repository root down to the working directory is read, the
// nearest last, so what is most specific is what the model reads last.
//
// A file may also pull in another with an @path line, which is how a
// long standard is kept in one place and referred to from several. The
// imports are bounded — depth, cycles, and the project root — because
// instruction files are written by people who are not thinking about
// what a harness will do with a symlink.

const (
	// maxImportDepth bounds how far an @path chain may go.
	maxImportDepth = 5
	// maxInstructionTotal bounds everything the instruction files
	// contribute, imports included.
	maxInstructionTotal = 64 * 1024
)

// Instruction is one instruction file that was found.
type Instruction struct {
	// Name is the path as the prompt names it: relative to the project
	// root when it is inside it.
	Name string
	Path string
	Text string
}

// Instructions reads every instruction file from the project root down
// to cwd, nearest last, with @path imports expanded. The project root is
// the repository's root when cwd is in one, and cwd itself otherwise:
// without a repository there is no boundary to walk up to, and walking
// to the filesystem root would read a stranger's home directory.
func Instructions(cwd string) []Instruction {
	cwd, err := filepath.Abs(cwd)
	if err != nil {
		return nil
	}
	root := ProjectRoot(cwd)
	var dirs []string
	for d := cwd; ; d = filepath.Dir(d) {
		dirs = append(dirs, d)
		if d == root || filepath.Dir(d) == d {
			break
		}
	}
	// Farthest first: the root's instructions, then each directory
	// closer in, so the most specific file is read last.
	var out []Instruction
	budget := maxInstructionTotal
	for i := len(dirs) - 1; i >= 0; i-- {
		in, ok := readInstruction(dirs[i], root, &budget)
		if ok {
			out = append(out, in)
		}
	}
	return out
}

// ProjectRoot is the repository root containing dir, or dir itself.
func ProjectRoot(dir string) string {
	for d := dir; ; d = filepath.Dir(d) {
		if fi, err := os.Stat(filepath.Join(d, ".git")); err == nil && (fi.IsDir() || fi.Mode().IsRegular()) {
			return d
		}
		if parent := filepath.Dir(d); parent == d {
			return dir
		}
	}
}

func readInstruction(dir, root string, budget *int) (Instruction, bool) {
	for _, name := range InstructionFiles {
		p := filepath.Join(dir, name)
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		seen := map[string]bool{resolvePath(p): true}
		text := strings.TrimSpace(expandImports(string(data), dir, root, seen, 1, budget))
		if text == "" {
			continue
		}
		if len(text) > maxInstructionBytes {
			text = textfmt.CutBytes(text, maxInstructionBytes) + "\n... (truncated)"
		}
		*budget -= len(text)
		label := name
		if rel, err := filepath.Rel(root, p); err == nil && !strings.HasPrefix(rel, "..") {
			label = filepath.ToSlash(rel)
		}
		return Instruction{Name: label, Path: p, Text: text}, true
	}
	return Instruction{}, false
}

// expandImports replaces @path lines with the file they name. A path is
// resolved against the importing file's directory, must stay inside the
// project root, and is read at most once per chain; anything else is
// left as the text it was, so a line that was never meant as an import
// still reads as what the author wrote.
func expandImports(text, dir, root string, seen map[string]bool, depth int, budget *int) string {
	if depth > maxImportDepth || *budget <= 0 {
		return text
	}
	lines := strings.Split(text, "\n")
	var b strings.Builder
	fenced := false
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			fenced = !fenced
		}
		out := line
		if ref, ok := importRef(line); ok && !fenced {
			if s, ok := importedText(ref, dir, root, seen, depth, budget); ok {
				out = s
			}
		}
		b.WriteString(out)
		if i < len(lines)-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// importRef reads an "@path" line, which is the whole line and nothing
// else: an @ in the middle of a sentence is prose, and an email address
// is not a file.
func importRef(line string) (string, bool) {
	t := strings.TrimSpace(line)
	if !strings.HasPrefix(t, "@") || len(t) < 2 {
		return "", false
	}
	ref := strings.TrimSpace(t[1:])
	if ref == "" || strings.ContainsAny(ref, " \t") {
		return "", false
	}
	return ref, true
}

// importedText is what an @ref line becomes. ok is false when ref names
// no file in the project (an @handle, a package name, a directory): the
// line was not an import, and is kept as the author wrote it.
func importedText(ref, dir, root string, seen map[string]bool, depth int, budget *int) (string, bool) {
	target := ref
	if !filepath.IsAbs(target) {
		target = filepath.Join(dir, target)
	}
	key := resolvePath(target)
	switch {
	case !within(key, root):
		return fmt.Sprintf("[import of %s skipped: it is outside the project]", ref), true
	case seen[key]:
		return fmt.Sprintf("[import of %s skipped: it is already included]", ref), true
	case depth >= maxImportDepth:
		return fmt.Sprintf("[import of %s skipped: imports are only followed %d deep]", ref, maxImportDepth), true
	}
	if fi, err := os.Stat(target); err != nil || !fi.Mode().IsRegular() {
		return "", false
	}
	data, err := os.ReadFile(target)
	if err != nil {
		return fmt.Sprintf("[import of %s skipped: %v]", ref, err), true
	}
	seen[key] = true
	body := strings.TrimSpace(string(data))
	if len(body) > *budget {
		body = textfmt.CutBytes(body, max(*budget, 0)) + "\n... (truncated)"
	}
	return expandImports(body, filepath.Dir(target), root, seen, depth+1, budget), true
}

// within reports whether p is root or inside it, after symlinks.
func within(p, root string) bool {
	root = resolvePath(root)
	if p == root {
		return true
	}
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func resolvePath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return filepath.Clean(r)
	}
	return filepath.Clean(p)
}
