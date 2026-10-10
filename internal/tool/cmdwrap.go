package tool

import (
	"context"
	"os/exec"
)

// A command wrap lets the agent put a foreground bash command under a
// monitor (package purity's strace) without the tool knowing what the
// monitor is: the agent puts a func on the call's context, and the tool
// hands it the built command just before it starts. A background job,
// a cached result and a remote workspace's command are never handed to
// it, so the agent learns nothing about them — which, for a monitor that
// fails closed, is the right answer.

type cmdWrapKey struct{}

// WithCommandWrap returns ctx carrying wrap.
func WithCommandWrap(ctx context.Context, wrap func(*exec.Cmd)) context.Context {
	return context.WithValue(ctx, cmdWrapKey{}, wrap)
}

// wrapCommand hands cmd to the wrap ctx carries, if any.
func wrapCommand(ctx context.Context, cmd *exec.Cmd) {
	if wrap, ok := ctx.Value(cmdWrapKey{}).(func(*exec.Cmd)); ok && wrap != nil {
		wrap(cmd)
	}
}
