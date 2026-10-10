package main

import (
	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/localize/tree"
)

// blindTester is the blind tester a headless run or an eval arm gets
// from blind_tester (config/blindtester.go), nil when that is off. Its
// throwaway copies are localize's: a detached worktree of the tree as it
// stands, uncommitted changes included.
func blindTester(cfg *config.Config) *agent.BlindTester {
	model, on := cfg.BlindTesterModel()
	if !on {
		return nil
	}
	return &agent.BlindTester{Model: model, Scratch: tree.Scratch}
}
