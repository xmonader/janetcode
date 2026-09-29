//go:build !linux

package shell

import (
	"context"
	"fmt"
	"os/exec"
)

// landlockABI is 0 off Linux: Landlock is a Linux LSM.
func landlockABI() int { return 0 }

func (s *Sandbox) landlock(context.Context, Profile, string, []string, string) (*exec.Cmd, error) {
	return nil, fmt.Errorf("Landlock is Linux only")
}

// landlockSocketFilter is false off Linux: there is no Landlock to pair it with.
const landlockSocketFilter = false
