//go:build !windows && !darwin

package control

import "errors"

// errSystemProxyUnsupported is returned by the system-proxy ops on platforms
// without an implementation (the Linux CI build). The daemon degrades gracefully:
// arming logs this and stays disarmed, so system-proxy mode simply doesn't take
// effect rather than crashing the core.
var errSystemProxyUnsupported = errors.New("control: system proxy is not supported on this platform")

func enableSystemProxy(string) error { return errSystemProxyUnsupported }

func disableSystemProxy() error { return errSystemProxyUnsupported }

// readSystemProxy reports "no proxy set" with no error so the startup reconcile
// finds nothing to clear rather than logging a spurious failure on every launch.
func readSystemProxy() (proxyState, error) { return proxyState{}, nil }
