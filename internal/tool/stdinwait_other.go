//go:build !linux

package tool

import "os"

// stdinWatchable is false off Linux: without /proc there is no way to see
// that a command is reading its stdin, so it gets EOF at once as before.
func stdinWatchable() bool { return false }

func pipeInode(f *os.File) uint64 { return 0 }

func probeStdin(sid int, ino uint64) inputState { return inputState{} }
