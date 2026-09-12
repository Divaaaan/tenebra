//go:build !windows && !darwin

package control

import "errors"

// errSystemProxyUnsupported is returned by the system-proxy ops on platforms
// without an implementation. Linux is the live case: pointing the desktop at a
// proxy there means writing per-desktop settings (GNOME's gsettings, KDE's
// kioslaverc, and a session's own environment) as the logged-in user, which a
// root daemon has no session bus to reach — a separate piece of work from
// bringing the tun path up. The daemon reports a local setup failure rather
// than promoting a connection whose OS proxy could not be applied. TUN remains
// the default supported mode.
var errSystemProxyUnsupported = errors.New("control: system proxy is not supported on this platform")

func enableSystemProxy(string) error { return errSystemProxyUnsupported }

func disableSystemProxy() error { return nil } // unsupported apply cannot mutate the OS

// readSystemProxy reports "no proxy set" with no error so the startup reconcile
// finds nothing to clear rather than logging a spurious failure on every launch.
func readSystemProxy() (proxyState, error) { return proxyState{}, nil }
