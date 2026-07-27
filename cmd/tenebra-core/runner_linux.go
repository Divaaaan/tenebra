//go:build linux

package main

import (
	"github.com/Divaaaan/tenebra/adapters/linux"
	"github.com/Divaaaan/tenebra/core/control"
)

// newRunner builds the tunnel supervisor for Linux. Linux gets its own adapter
// (rather than reusing the generic one in runner_other.go) because the two
// things that go wrong there are Linux-specific and worth naming in the logs the
// user sees: the tun device needs privilege the Windows wintun path does not,
// and /dev/net/tun may not exist at all. See adapters/linux.
func newRunner() control.Runner {
	return linux.New()
}
