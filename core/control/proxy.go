package control

import (
	"errors"
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
// Enable must retain enough ownership information to restore any partially
// applied change. Disable restores that snapshot and is harmless when Enable
// failed before changing anything. A failed Disable must retain its snapshot.
type systemProxyController interface {
	// Enable points the OS at hostport (the loopback mixed inbound).
	Enable(hostport string) error
	// Disable restores the configuration owned by this controller.
	Disable() error
	// Get reads the current OS proxy configuration. It backs the startup reconcile
	// that clears a proxy a previous run left pointing at our mixed inbound.
	Get() (proxyState, error)
}

// armSystemProxy confirms apply before the connection can be promoted. Ownership
// starts before Enable because an error can follow a partial OS mutation.
func (d *Daemon) armSystemProxy(hostport string) error {
	d.proxyMu.Lock()
	defer d.proxyMu.Unlock()
	d.mu.Lock()
	already := d.proxyApplied && d.proxyTarget == hostport
	pending := d.proxyArmed
	d.mu.Unlock()
	if already {
		return nil
	}
	if pending {
		if err := d.disarmSystemProxyLocked(); err != nil {
			return fmt.Errorf("restore previous system proxy before applying: %w", err)
		}
	}
	d.mu.Lock()
	d.proxyArmed = true
	d.mu.Unlock()
	if err := d.proxy.Enable(hostport); err != nil {
		rollback := d.disarmSystemProxyLocked()
		if rollback != nil {
			rollback = fmt.Errorf("rollback system proxy: %w", rollback)
		}
		return errors.Join(err, rollback)
	}
	d.mu.Lock()
	d.proxyApplied = true
	d.proxyTarget = hostport
	d.mu.Unlock()
	d.emitLog(LogInfo, "system proxy: OS now routing through "+hostport)
	return nil
}

// disarmSystemProxy clears the OS proxy pointer if (and only if) we armed it,
// restoring direct connectivity. It is the guard's teardown: every path that
// leaves a system-proxy connection — an explicit disconnect, a tunnel-process
// death, connect supersession, and daemon shutdown — funnels through it, so the
// OS is never left pointing at a mixed inbound that is no longer listening. It is
// idempotent. A failure retains ownership so a later disconnect/startup can retry.
func (d *Daemon) disarmSystemProxy() error {
	d.proxyMu.Lock()
	defer d.proxyMu.Unlock()
	return d.disarmSystemProxyLocked()
}

func (d *Daemon) disarmSystemProxyLocked() error {
	d.mu.Lock()
	armed := d.proxyArmed
	d.proxyApplied = false
	d.mu.Unlock()
	if !armed {
		return nil
	}
	if err := d.proxy.Disable(); err != nil {
		d.emitLog(LogError, fmt.Sprintf("system proxy: could not restore previous settings: %v; cleanup remains pending", err))
		return err
	}
	d.mu.Lock()
	d.proxyArmed = false
	d.proxyTarget = ""
	d.mu.Unlock()
	d.emitLog(LogInfo, "system proxy: previous settings restored")
	return nil
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
	d.proxyMu.Lock()
	defer d.proxyMu.Unlock()
	if owned, ok := d.proxy.(interface{ Reconcile() (bool, error) }); ok {
		found, restoreErr := owned.Reconcile()
		d.mu.Lock()
		if found {
			d.proxyArmed = restoreErr != nil
			d.proxyApplied = false
			d.proxyTarget = ""
		}
		d.mu.Unlock()
		return found && restoreErr == nil, restoreErr
	}
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

// ReconcileSystemProxyWhenIdle handles a console logon after service startup.
// Session notifications must not block the SCM handler or race a new connect.
func (d *Daemon) ReconcileSystemProxyWhenIdle() {
	if !d.connMu.TryLock() {
		return
	}
	defer d.connMu.Unlock()
	st := d.snapshotState()
	if st.State != StateIdle && st.State != StateError {
		return
	}
	if cleared, err := d.ReconcileSystemProxyAtStartup(); err != nil {
		d.emitLog(LogWarn, fmt.Sprintf("system proxy session restore: %v", err))
		d.mu.Lock()
		pending := d.proxyArmed
		d.mu.Unlock()
		if pending {
			st.State = StateError
			st.Error = "system proxy restore remains pending: " + err.Error()
			d.setState(st)
		}
	} else if cleared {
		d.emitLog(LogInfo, "system proxy: recovered previous user settings after logon")
	}
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
