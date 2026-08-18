//go:build linux

package main

import (
	"github.com/Divaaaan/tenebra/adapters/linux"
	"github.com/Divaaaan/tenebra/core/control"
	"github.com/Divaaaan/tenebra/core/tunguard"
)

// newRunner builds the tunnel supervisor for Linux. Linux gets its own adapter
// (rather than reusing the generic one in runner_other.go) because the two
// things that go wrong there are Linux-specific and worth naming in the logs the
// user sees: the tun device needs privilege the Windows wintun path does not,
// and /dev/net/tun may not exist at all. See adapters/linux.
func newRunner() control.Runner {
	return linux.New()
}

// newInterfaceProbe returns nil: the tun-conflict guard has no Linux route
// enumeration yet, and nil leaves it disabled rather than guessing.
//
// Disabled, not "assume clear with a warning": the guard's value is naming the
// interface in the way, and a check that cannot read the route table has nothing
// to name. When adapters/linux grows a netlink (RTM_GETROUTE) enumerator, wiring
// it here is the only change needed.
func newInterfaceProbe() func() ([]tunguard.Iface, error) {
	return nil
}
