//go:build !darwin && !windows && !linux

package control

import "syscall"

// tunIfaceName is the branded tun name used off macOS. On a platform without a
// per-socket interface-bind primitive it only feeds the name-based exclusion in
// selectDefaultInterface; the bind itself is a no-op below.
const tunIfaceName = "tenebra"

// bindSocketToInterface is a no-op on platforms this project has no bind
// primitive for: the ping falls back to an ordinary routed dial. Returning nil
// (rather than an error) keeps ping working — with the routing-table caveat the
// bound platforms avoid — instead of failing every probe on an unsupported OS.
func bindSocketToInterface(c syscall.RawConn, iface ifaceInfo, network string) error {
	return nil
}
