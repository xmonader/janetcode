//go:build !linux

package shell

import "fmt"

func egressBridgeSupported() bool { return false }

func bridgeArgv(string, int, []string) ([]string, error) {
	return nil, fmt.Errorf("the egress bridge is Linux only")
}
