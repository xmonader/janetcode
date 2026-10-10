package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/preresolve"
	"github.com/xmonader/janetcode/internal/prompt"
	"github.com/xmonader/janetcode/internal/repomap"
	"github.com/xmonader/janetcode/internal/tool"
)

// wirePreresolve adds symbol pre-resolution (config preresolve) to the
// turn preparation: with a fresh session's first message, the files and
// identifiers it names, resolved against the repository map's index, and
// the tests that exercise them. A repository already sent whole has
// nothing left to resolve, so the hook stands aside for it.
func wirePreresolve(b *agentBuilder) {
	if !b.d.cfg.Preresolve {
		return
	}
	ag, repo, seen := b.ag, b.repo, b.seen
	ag.Prepare = chainPrep(ag.Prepare, func(ctx context.Context, blocks []llm.Block) ([]llm.Block, string) {
		return preresolveFirstTurn(ctx, ag, repo, seen, blocks)
	})
}

func preresolveFirstTurn(ctx context.Context, ag *agent.Agent, repo *repomap.Cache, seen *tool.Seen, blocks []llm.Block) ([]llm.Block, string) {
	task := firstTextOf(blocks)
	if len(ag.Messages) != 0 || repo == nil || strings.TrimSpace(task) == "" || attachedWhole(blocks) {
		return blocks, ""
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	ix, err := repo.Get(ctx)
	if err != nil {
		return blocks, ""
	}
	res := preresolve.Resolve(ix, task, 0)
	if res.Text == "" {
		return blocks, ""
	}
	// A file sent whole is as read as one read_file returned: an edit to
	// it needs no read first, and a later change to it on disk is caught.
	var read []string
	for rel, data := range res.Whole {
		abs := filepath.Join(ix.Root, filepath.FromSlash(rel))
		seen.Record(abs, data)
		read = append(read, rel)
	}
	ag.NoteRead(read...)
	note := fmt.Sprintf("resolved from the request: %d definition(s), %d test(s), %d file(s) whole", len(res.Symbols), len(res.Tests), len(res.Whole))
	return append(blocks, llm.Block{Type: llm.BlockText, Text: res.Text, Synthetic: true}), note
}

// attachedWhole reports whether firstTurnRepo already sent the
// repository whole with this message.
func attachedWhole(blocks []llm.Block) bool {
	for _, b := range blocks {
		if b.Synthetic && strings.HasPrefix(b.Text, "<repository files=") {
			return true
		}
	}
	return false
}

// wirePrefixKernel switches the session to the prefix kernel (config
// prefix_kernel; see prompt.KernelSections): the system prompt loses its
// environment and gains the committed tree's map, and the environment
// goes to the end of the first user message instead, below everything a
// second session on the same commit would send identically. It must be
// the last hook wired, so the note is the last block of that message.
//
// Whatever stepCore appended after the prompt (the conflict-exit line) is
// kept as it was. A /cd rebuilds the usual prompt for the new directory,
// which is the right prompt to have there: the cached prefix is lost to
// the move either way.
func wirePrefixKernel(b *agentBuilder) {
	if !b.d.cfg.PrefixKernel {
		return
	}
	ag, d := b.ag, b.d
	usual := prompt.SystemWith(d.cwd, d.promptOverride)
	if !strings.HasPrefix(ag.System, usual) {
		return // something rebuilt the prompt; leave it alone
	}
	store := ""
	if d.sessDir != "" {
		store = filepath.Join(filepath.Dir(d.sessDir), "kernels")
	}
	m, _ := repomap.Kernel(context.Background(), b.repo, store)
	ag.System = prompt.KernelSystem(d.cwd, d.promptOverride, m) + ag.System[len(usual):]
	cwd := d.cwd
	ag.Prepare = chainPrep(ag.Prepare, func(_ context.Context, blocks []llm.Block) ([]llm.Block, string) {
		if len(ag.Messages) != 0 || strings.TrimSpace(firstTextOf(blocks)) == "" {
			return blocks, ""
		}
		return append(blocks, llm.Block{Type: llm.BlockText, Text: prompt.EnvironmentNote(cwd), Synthetic: true}), ""
	})
}
