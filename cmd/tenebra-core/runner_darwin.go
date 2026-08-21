//go:build darwin

package main

import (
	"github.com/Divaaaan/tenebra/adapters/macos"
	"github.com/Divaaaan/tenebra/core/control"
	"github.com/Divaaaan/tenebra/core/tunguard"
)

// newRunner builds the tunnel supervisor for macOS. macOS gets its own adapter
// (rather than reusing the generic one in runner_other.go) because opening the
// utun device requires privilege the Windows wintun path does not — see
// adapters/macos and docs/porting/macos.md.
func newRunner() control.Runner {
	return macos.New()
}

// newInterfaceProbe returns nil: the tun-conflict guard has no macOS route
// enumeration yet, and nil leaves it disabled rather than guessing.
//
// Disabled, not "assume clear with a warning": the guard's whole value is that
// it names the interface in the way, and a check that cannot see the route table
// has nothing to name. When adapters/macos grows a `route -n get default`-based
// enumerator, wiring it here is the only change needed.
func newInterfaceProbe() func() ([]tunguard.Iface, error) {
	return nil
}
