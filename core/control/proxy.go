package control

import (
	"fmt"
	"net"
	"strings"
)

// proxyState snapshots the OS proxy configuration the guard cares about: whether
// a system proxy is enabled and the host:port it points at. The platform readers
// fill it from the registry (Windows) or networksetup (macOS); the stub leaves it
// zero. Server is empty when no proxy is set.
type proxyState struct {
	Enabled bool
	Server  string
}

// systemProxyController applies, reads, and clears the OS-wide HTTP/SOCKS proxy
// pointer that system-proxy mode relies on. Every method MUST be idempotent and
// safe to call when the OS is already in the desired state, because the daemon
// drives them from connect/disconnect/crash paths that can repeat. The
// implementation is platform-specific (registry on Windows, networksetup on
// macOS, a no-op stub elsewhere) and hidden behind this interface so the guard
// sequencing is unit-testable with a fake — the real registry/networksetup calls
// run only in a live session, never a unit test.
//
// The guard deliberately toggles the proxy on and off rather than saving and
// restoring a user's pre-existing proxy: a machine that already routes through a
// corporate proxy is not a machine that also needs this mode, and capture/restore
// adds a second failure surface. See the report's "left for live acceptance" note.
type systemProxyController interface {
	// Enable points the OS at hostport (the loopback mixed inbound).
	Enable(hostport string) error
	// Disable removes the proxy pointer, restoring direct connectivity.
	Disable() error
	// Get reads the current OS proxy configuration. It backs the startup reconcile
	// that clears a proxy a previous run left pointing at our mixed inbound.
	Get() (proxyState, error)
}

// realSystemProxy is the production controller. Its methods defer to the
// build-tagged platform functions (proxy_windows.go / proxy_darwin.go /
// proxy_other.go), mirroring how newPingDialer defers to bindSocketToInterface.
type realSystemProxy struct{}

func (realSystemProxy) Enable(hostport string) error { return enableSystemProxy(hostport) }
func (realSystemProxy) Disable() error               { return disableSystemProxy() }
func (realSystemProxy) Get() (proxyState, error)     { return readSystemProxy() }

var _ systemProxyController = realSystemProxy{}

// armSystemProxy points the OS at hostport and records that WE now own the proxy
// pointer, so disarmSystemProxy later knows to clear it. It is idempotent: a
// second call while already armed is a no-op, so a hot-swap that re-promotes the
// same connection doesn't rewrite the registry. A failure to enable is logged and
// leaves the guard disarmed — the tunnel is up but the OS still routes direct,
// which the user sees as "connected but not protected", a visible, safe failure
// rather than a half-set proxy.
func (d *Daemon) armSystemProxy(hostport string) {
	d.mu.Lock()
	already := d.proxyArmed
	d.mu.Unlock()
	if already {
		return
	}
	if err := d.proxy.Enable(hostport); err != nil {
		d.emitLog(LogError, fmt.Sprintf("system proxy: could not point the OS at %s: %v", hostport, err))
		return
	}
	d.mu.Lock()
	d.proxyArmed = true
	d.mu.Unlock()
	d.emitLog(LogInfo, "system proxy: OS now routing through "+hostport)
}

// disarmSystemProxy clears the OS proxy pointer if (and only if) we armed it,
// restoring direct connectivity. It is the guard's teardown: every path that
// leaves a system-proxy connection — an explicit disconnect, a tunnel-process
// death, connect supersession, and daemon shutdown — funnels through it, so the
// OS is never left pointing at a mixed inbound that is no longer listening. It is
// idempotent (a no-op when not armed) and clears the armed flag up front, so a
// Disable error can't wedge the guard into retrying forever; a persistent failure
// is logged loudly and the next startup's reconcile is the backstop.
func (d *Daemon) disarmSystemProxy() {
	d.mu.Lock()
	armed := d.proxyArmed
	d.proxyArmed = false
	d.mu.Unlock()
	if !armed {
		return
	}
	if err := d.proxy.Disable(); err != nil {
		d.emitLog(LogError, fmt.Sprintf("system proxy: could not restore direct connectivity: %v; turn the proxy off in OS network settings", err))
		return
	}
	d.emitLog(LogInfo, "system proxy: cleared; OS back to direct")
}

// ReconcileSystemProxyAtStartup clears a system proxy a previous run left pointing
// at our loopback mixed inbound — the backstop for the one teardown path the
// daemon cannot run itself: a hard kill (SIGKILL, power loss, or a crash the
// deferred cleanup can't catch) of the core while the proxy was armed. Without it
// the machine would come up with the OS pointed at a dead local proxy and no
// internet. It returns whether it cleared anything so the caller can log it.
//
// It is deliberately conservative: it clears the proxy ONLY when the OS currently
// points at exactly the address this build would set (our loopback host:port). A
// remote or PAC proxy — a corporate proxy the user needs — never matches, and
// neither does another local tool's proxy on a different port, so this never
// touches a proxy tenebra did not set. main calls it once at startup, before
// serving, while the daemon is idle. It never arms anything.
func (d *Daemon) ReconcileSystemProxyAtStartup() (cleared bool, err error) {
	st, err := d.proxy.Get()
	if err != nil {
		return false, fmt.Errorf("read OS proxy state: %w", err)
	}
	want := d.snapshotTun().MixedHostPort()
	if !st.Enabled || !sameProxyTarget(st.Server, want) {
		return false, nil
	}
	if err := d.proxy.Disable(); err != nil {
		return false, fmt.Errorf("clear stale proxy %q: %w", st.Server, err)
	}
	return true, nil
}

// sameProxyTarget reports whether two proxy server strings name the same
// host:port, comparing case-insensitively on host and ignoring surrounding
// whitespace. An unparseable or portless value on either side is treated as "not
// the same", so the reconcile stays hands-off unless the match is exact — the
// safe bias for a routine that turns the user's connectivity off.
func sameProxyTarget(got, want string) bool {
	g := normalizeHostPort(got)
	return g != "" && g == normalizeHostPort(want)
}

// normalizeHostPort lowercases the host and rejoins host:port, or returns "" when
// the input is not a host:port pair.
func normalizeHostPort(s string) string {
	h, p, err := net.SplitHostPort(strings.TrimSpace(s))
	if err != nil {
		return ""
	}
	return strings.ToLower(h) + ":" + p
}
