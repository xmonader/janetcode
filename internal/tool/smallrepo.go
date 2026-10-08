package tool

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Limits for inlining a whole repository into a session's first message.
// A repository this small is read whole by nearly every coding turn
// anyway, one read_file round trip at a time; sending it up front saves
// those calls and the framing each one re-sends.
const (
	SmallRepoMaxBytes = 32 << 10
	SmallRepoMaxFiles = 40
)

// SmallRepo returns the whole repository under root as one block of text,
// with every file in it recorded in seen as read, or "" when the
// repository is over the limits, empty, or not on the local disk. Files
// git ignores and files the AI ignore files exclude are left out, binary
// files are named without their bytes, and nothing is read from outside
// root.
func SmallRepo(ctx context.Context, ws *Workspace, root string, seen *Seen) (text string, files int) {
	if root == "" || (ws != nil && !ws.Local()) {
		return "", 0
	}
	rels, err := repoFiles(ctx, root)
	if err != nil || len(rels) == 0 || len(rels) > SmallRepoMaxFiles {
		return "", 0
	}
	type entry struct {
		rel, abs string
		data     []byte
	}
	var (
		entries []entry
		binary  []string
		total   int
	)
	for _, rel := range rels {
		abs, err := resolveOn(ws, root, rel)
		if err != nil {
			continue // ignored by .aiignore, or reaching outside root
		}
		st, err := os.Lstat(abs)
		if err != nil || !st.Mode().IsRegular() {
			continue
		}
		if total += int(st.Size()); total > SmallRepoMaxBytes {
			return "", 0
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			return "", 0
		}
		if isBinary(data) {
			binary = append(binary, rel)
			continue
		}
		entries = append(entries, entry{rel, abs, data})
	}
	if len(entries) == 0 {
		return "", 0
	}
	var b strings.Builder
	fmt.Fprintf(&b, "<repository files=\"%d\">\nThe whole repository, as it is now. These are the files' current contents: do not read them again.\n", len(entries))
	for _, e := range entries {
		fmt.Fprintf(&b, "<file path=%q>\n%s", e.rel, e.data)
		if len(e.data) > 0 && e.data[len(e.data)-1] != '\n' {
			b.WriteByte('\n')
		}
		b.WriteString("</file>\n")
	}
	if len(binary) > 0 {
		fmt.Fprintf(&b, "Binary, not shown: %s\n", strings.Join(binary, ", "))
	}
	b.WriteString("</repository>")
	for _, e := range entries {
		seen.Record(e.abs, e.data)
	}
	return b.String(), len(entries)
}

// repoFiles lists the files under root that git does not ignore, or walks
// the tree, skipping hidden and dependency directories, without git.
// Either way the walk stops past the file limit: a large tree is not
// inlined, so there is no point listing all of it.
func repoFiles(ctx context.Context, root string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", root, "ls-files", "-co", "--exclude-standard", "-z")
	if raw, err := cmd.Output(); err == nil {
		var out []string
		for _, p := range bytes.Split(raw, []byte{0}) {
			if len(p) > 0 {
				out = append(out, string(p))
			}
		}
		sort.Strings(out)
		return out, nil
	}
	var out []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if err != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if p != root && (strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" || name == "target" || name == "dist" || name == "build" || name == "__pycache__") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(name, ".") {
			return nil
		}
		if rel, err := filepath.Rel(root, p); err == nil {
			out = append(out, filepath.ToSlash(rel))
		}
		if len(out) > SmallRepoMaxFiles {
			return filepath.SkipAll
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}
