package protection

import (
	"errors"
	"sync"
)

// State reports confirmed policy independently of the desired setting.
type State struct {
	Status     string `json:"status"`
	Enforced   bool   `json:"enforced"`
	Persistent bool   `json:"persistent"`
	Error      string `json:"error,omitempty"`
}

// Backend changes only owned policy. Replace and Remove must be atomic; a
// failed operation leaves the previous policy unchanged. Inspect verifies the
// complete four-layer default block, not just the existence of a provider.
type Backend interface {
	Inspect() (present, complete bool, err error)
	Replace(tunLUID uint64) error
	ResolveTunnel(name, address string) (uint64, error)
	Remove() error
}

type Guard struct {
	op      sync.Mutex
	mu      sync.Mutex
	backend Backend
	state   State
	notify  func()
	tunnel  *verifiedTunnel
}

type verifiedTunnel struct {
	name, address string
	luid          uint64
}

func New(b Backend) *Guard {
	g := &Guard{backend: b, state: State{Status: "off"}}
	if b == nil {
		g.state.Status = "unavailable"
	}
	return g
}

// SetNotify is configured once before commands begin. The callback runs without
// Guard.mu and may safely read Snapshot; it must not perform another operation.
func (g *Guard) SetNotify(f func()) { g.notify = f }
func (g *Guard) Snapshot() State    { g.mu.Lock(); defer g.mu.Unlock(); return g.state }
func (g *Guard) set(s State) {
	g.mu.Lock()
	g.state = s
	g.mu.Unlock()
	if g.notify != nil {
		g.notify()
	}
}
func (g *Guard) confirm(s State, tunnel *verifiedTunnel) {
	g.mu.Lock()
	g.state, g.tunnel = s, tunnel
	g.mu.Unlock()
	if g.notify != nil {
		g.notify()
	}
}
func (g *Guard) pending() { s := g.Snapshot(); s.Status = "applying"; s.Error = ""; g.set(s) }
func (g *Guard) fail(err error) error {
	s := g.Snapshot()
	s.Status = "error"
	s.Error = err.Error()
	g.set(s)
	return err
}

// Reject reports a configuration refusal before native policy is touched.
// It preserves the last confirmed enforcement just like an apply failure.
func (g *Guard) Reject(err error) error { return g.fail(err) }
func (g *Guard) available() error {
	if g.backend == nil {
		return errors.New("persistent host protection is unavailable on this platform")
	}
	return nil
}

// Recover never clears an existing guard on the strength of a preferences file.
// A crash may have happened between saving OFF and removing the policy.
func (g *Guard) Recover() error {
	g.op.Lock()
	defer g.op.Unlock()
	if err := g.available(); err != nil {
		return err
	}
	present, complete, err := g.backend.Inspect()
	if err != nil {
		return g.fail(err)
	}
	if !present {
		g.confirm(State{Status: "off"}, nil)
		return nil
	}
	g.set(State{Status: "blocked", Enforced: complete, Persistent: complete})
	return g.prepare()
}

// Prepare closes TUN allowances before a process replacement, retaining the
// trusted engine/core bootstrap path. It is deliberately separate from Stop.
func (g *Guard) Prepare() error {
	g.op.Lock()
	defer g.op.Unlock()
	return g.prepare()
}
func (g *Guard) prepare() error {
	if err := g.available(); err != nil {
		return err
	}
	g.pending()
	if err := g.backend.Replace(0); err != nil {
		return g.fail(err)
	}
	g.confirm(State{Status: "blocked", Enforced: true, Persistent: true}, nil)
	return nil
}

// VerifyTunnel is called only after the engine's successful probe. Resolving a TUN
// failure leaves lockdown in place; there is no fallback to a name or address.
func (g *Guard) VerifyTunnel(name, address string, systemProxy bool) error {
	g.op.Lock()
	defer g.op.Unlock()
	if err := g.available(); err != nil {
		return err
	}
	var luid uint64
	if !systemProxy {
		var err error
		luid, err = g.backend.ResolveTunnel(name, address)
		if err != nil {
			return g.fail(err)
		}
		if luid == 0 {
			return g.fail(errors.New("TUN has no verified interface identity"))
		}
	}
	g.pending()
	if err := g.backend.Replace(luid); err != nil {
		return g.fail(err)
	}
	g.confirm(State{Status: "blocked", Enforced: true, Persistent: true}, &verifiedTunnel{name, address, luid})
	return nil
}

// TunnelPresent checks the same LUID, name and address that were permitted.
// A newly-created same-name interface cannot stand in for the verified one.
// checked=false leaves non-TUN/unprotected platforms to their existing watcher.
func (g *Guard) TunnelPresent() (checked, present bool) {
	g.mu.Lock()
	tunnel := g.tunnel
	g.mu.Unlock()
	if tunnel == nil || tunnel.luid == 0 {
		return false, false
	}
	luid, err := g.backend.ResolveTunnel(tunnel.name, tunnel.address)
	g.mu.Lock()
	unchanged := tunnel == g.tunnel
	g.mu.Unlock()
	if !unchanged {
		return true, true
	} // the next tick checks the new policy
	return true, err == nil && luid == tunnel.luid
}

// Accepted marks the already-verified engine only after ALL local gates (including
// system proxy) pass. The caller publishes its connected state immediately after
// this; no intermediate active event is emitted over an unaccepted connection.
func (g *Guard) Accepted() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.state.Status == "blocked" && g.state.Enforced && g.tunnel != nil {
		g.state.Status = "active"
	}
}

// Interrupted is metadata only. Persistent kernel policy requires no userspace
// cleanup to survive a crash, a stopped service or the relaunch limit.
func (g *Guard) Interrupted() {
	g.mu.Lock()
	changed := g.state.Status == "active"
	if changed {
		g.state.Status = "blocked"
	}
	g.mu.Unlock()
	if changed && g.notify != nil {
		g.notify()
	}
}

// Release is reserved for explicit OFF/Disconnect/uninstall, never Close.
func (g *Guard) Release() error {
	g.op.Lock()
	defer g.op.Unlock()
	if g.backend == nil {
		return nil
	}
	g.pending()
	if err := g.backend.Remove(); err != nil {
		return g.fail(err)
	}
	g.confirm(State{Status: "off"}, nil)
	return nil
}
