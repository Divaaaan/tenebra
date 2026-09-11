//go:build !windows

package windows

import "os/exec"

func startOwnedCommand(cmd *exec.Cmd) (func() error, error) {
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return func() error { return nil }, nil
}
