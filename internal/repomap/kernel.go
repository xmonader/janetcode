package repomap

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// A kernel map is the repository map rendered for no request at all, so
// that every session on the same tree can put the same bytes in its
// system prompt and a provider's prompt cache matches it from the first
// call (see prompt.KernelSections). The per-turn map still follows each
// request; this one is the part every request shares.
//
// The bytes are kept on disk under the tree's hash, not re-rendered per
// session: a map is a ranking over floats and a parse of whatever the
// working tree holds, and one changed byte anywhere in it would cost the
// whole cached prefix that sits below it. Rendering it once and reading
// it back is what makes "byte-identical" a property of the store rather
// than a hope about the ranker.

// kernelVersion is in every key, so a change to how the kernel is rendered
// cannot hand a new build an old build's bytes.
const kernelVersion = "k1"

// KernelBudget is the size of the kernel map, in bytes: the per-turn map's
// budget, since it plays the same part for a session that does not yet
// have a request.
const KernelBudget = DefaultBudget

// KernelMinFiles is the smallest index a kernel map is written for. A
// repository this small is sent whole with the first message
// (tool.SmallRepo), and a map of it would only repeat the file list.
const KernelMinFiles = 41

// TreeKey names the committed tree at root: the hash of HEAD's tree for
// root's directory, which two checkouts of the same content share
// whatever their commits are called. ok is false outside git, or with no
// commit yet.
func TreeKey(ctx context.Context, root string) (key string, ok bool) {
	out, err := exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "HEAD:./").Output()
	if err != nil {
		return "", false
	}
	key = strings.TrimSpace(string(out))
	if key == "" || strings.ContainsAny(key, "/\\ \n") {
		return "", false
	}
	return key, true
}

// Kernel returns the kernel map for the tree c is rooted at, and the tree
// key it is filed under. A map already stored for the key is returned as
// it was written. Otherwise one is rendered, and stored only when the
// working tree is clean: a map rendered from uncommitted changes would be
// filed under a tree it does not describe, and every later session on
// that commit would inherit them. A dirty tree with nothing stored gets a
// map rendered now and kept by nobody.
//
// "" means no kernel map: outside git, an index too small to need one,
// or a build that did not finish within the bound. store "" keeps
// nothing on disk.
func Kernel(ctx context.Context, c *Cache, store string) (text, key string) {
	if c == nil {
		return "", ""
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	key, ok := TreeKey(ctx, c.Root)
	if !ok {
		return "", ""
	}
	file := ""
	if store != "" {
		file = filepath.Join(store, kernelVersion+"-"+key+".map")
		if data, err := os.ReadFile(file); err == nil {
			return string(data), key
		}
	}
	ix, err := c.getBounded(ctx)
	if err != nil || len(ix.Files) < KernelMinFiles {
		return "", key
	}
	text = ix.renderKernel(key)
	if text == "" {
		return "", key
	}
	if file != "" && len(ChangedFiles(ctx, ix.Root)) == 0 {
		kernelWrite(file, text)
	}
	return text, key
}

// renderKernel is Render over a ranking with no request in it, under a
// heading that says so: the per-turn heading's "ranked for this request"
// would be false here.
func (ix *Index) renderKernel(key string) string {
	m := ix.Render(ix.Rank("", nil), KernelBudget)
	if m == "" {
		return ""
	}
	if i := strings.IndexByte(m, '\n'); i >= 0 {
		m = m[i+1:]
	} else {
		return ""
	}
	short := key
	if len(short) > 12 {
		short = short[:12]
	}
	head := fmt.Sprintf("[repository map of the committed tree %s: %d source files ranked by structure alone; symbols ordered by how often other files use them. Uncommitted changes are not in it; each request's own map follows it]\n", short, len(ix.Files))
	if ix.Total > len(ix.Files) {
		head = fmt.Sprintf("[repository map of the committed tree %s: %d of %d source files (a large tree: the rest sampled evenly; grep or glob for what is not here), ranked by structure alone. Uncommitted changes are not in it; each request's own map follows it]\n", short, len(ix.Files), ix.Total)
	}
	return head + m
}

// kernelWrite stores text atomically: a session reading the store while
// another writes it must see the old bytes or the new ones, never half.
// A failure is dropped, since the store only saves a later render.
func kernelWrite(file, text string) {
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(file), ".kernel-*")
	if err != nil {
		return
	}
	_, werr := tmp.WriteString(text)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		os.Remove(tmp.Name())
		return
	}
	if err := os.Rename(tmp.Name(), file); err != nil {
		os.Remove(tmp.Name())
	}
}
