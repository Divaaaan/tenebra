package control

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Divaaaan/tenebra/core/singbox"
)

// These tests cover the kill switch and the tun-stack option end to end at the
// protocol level: recording and reporting the choices, arming strict_route in
// the built config, hot-swapping a live tunnel in place (same node, no fallback
// walk), relaunching a tunnel whose process died while the switch was armed,
// and persisting both preferences across a daemon restart.

// tunFromConfig extracts strict_route and the stack from the (single) tun
// inbound of a built config.
func tunFromConfig(t *testing.T, cfgJSON []byte) (strictRoute bool, stack string) {
	t.Helper()
	var cfg struct {
		Inbounds []map[string]any `json:"inbounds"`
	}
	if err := json.Unmarshal(cfgJSON, &cfg); err != nil {
		t.Fatalf("parse config: %v", err)
	}
	for _, in := range cfg.Inbounds {
		if in["type"] == "tun" {
			sr, _ := in["strict_route"].(bool)
			st, _ := in["stack"].(string)
			return sr, st
		}
	}
	t.Fatal("no tun inbound in config")
	return false, ""
}

// awaitLogContains drains log events until one carries the substring.
func (h *harness) awaitLogContains(sub string) {
	h.t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case ev := <-h.events:
			if ev["event"] != EventLog {
				continue
			}
			if msg, _ := ev["msg"].(string); strings.Contains(msg, sub) {
				return
			}
		case <-deadline:
			h.t.Fatalf("timed out waiting for log containing %q", sub)
		}
	}
}

// TestSetKillSwitchIdleAppliesOnNextConnect: arming while idle is recorded,
// reported, and lands as strict_route in the next connect's config.
func TestSetKillSwitchIdleAppliesOnNextConnect(t *testing.T) {
	h := newHarness(t)
	p := seedMultiProto(t, h)

	h.send(Request{ID: 1, Cmd: CmdSetKillSwitch, On: true})
	var st State
	h.dataInto(h.await(), &st)
	if !st.KillSwitch {
		t.Fatalf("set_kill_switch response kill_switch = false, want true")
	}
	if h.runner.starts() != 0 {
		t.Fatalf("arming while idle started a process (starts=%d)", h.runner.starts())
	}

	h.send(Request{ID: 2, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	h.awaitState(StateConnected)

	strict, _ := tunFromConfig(t, h.runner.startCfgs()[0])
	if !strict {
		t.Error("config built with kill switch armed lacks strict_route")
	}
}

// TestKillSwitchDefaultsOff: without arming, the built config must not carry
// strict_route — the rough-connect trade-off is strictly opt-in.
func TestKillSwitchDefaultsOff(t *testing.T) {
	h := newHarness(t)
	p := seedMultiProto(t, h)

	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	h.awaitState(StateConnected)

	strict, stack := tunFromConfig(t, h.runner.startCfgs()[0])
	if strict {
		t.Error("strict_route on by default; the kill switch must be opt-in")
	}
	if stack != "system" {
		t.Errorf("default stack = %q, want system", stack)
	}
}

// TestSetKillSwitchLiveHotSwapsSameNode: arming while connected restarts
// sing-box once, pinned to the node already in use, and comes back connected
// with strict_route in the new config.
func TestSetKillSwitchLiveHotSwapsSameNode(t *testing.T) {
	h := newHarness(t)
	p := seedMultiProto(t, h)

	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	connected := h.awaitState(StateConnected)
	node := connected["node"].(string)

	h.send(Request{ID: 2, Cmd: CmdSetKillSwitch, On: true})
	var st State
	h.dataInto(h.await(), &st)
	if !st.KillSwitch {
		t.Fatal("response kill_switch = false, want true")
	}

	// The swap dips through connecting and lands connected on the same node.
	re := h.awaitState(StateConnected)
	if re["node"] != node {
		t.Errorf("reconnected node = %v, want the same node %s", re["node"], node)
	}

	cfgs := h.runner.startCfgs()
	if len(cfgs) != 2 {
		t.Fatalf("starts = %d, want 2 (one connect, one hot swap)", len(cfgs))
	}
	if strict, _ := tunFromConfig(t, cfgs[0]); strict {
		t.Error("initial config already had strict_route")
	}
	if strict, _ := tunFromConfig(t, cfgs[1]); !strict {
		t.Error("hot-swapped config lacks strict_route")
	}
	if tagBefore, tagAfter := connectedNodeTag(t, cfgs[0]), connectedNodeTag(t, cfgs[1]); tagBefore != tagAfter {
		t.Errorf("hot swap moved the selector: %q -> %q; it must pin the current node", tagBefore, tagAfter)
	}
}

// TestSetKillSwitchUnchangedDoesNotRestart: re-sending the current value must
// not bounce a live tunnel.
func TestSetKillSwitchUnchangedDoesNotRestart(t *testing.T) {
	h := newHarness(t)
	p := seedMultiProto(t, h)

	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	h.awaitState(StateConnected)

	h.send(Request{ID: 2, Cmd: CmdSetKillSwitch, On: false}) // already off
	h.await()
	if h.runner.starts() != 1 {
		t.Errorf("starts = %d, want 1 (no-op toggle must not restart)", h.runner.starts())
	}
}

// TestSetTunRejectsUnknownStack: the stack is validated before anything is
// recorded, so junk can never reach sing-box.
func TestSetTunRejectsUnknownStack(t *testing.T) {
	h := newHarness(t)

	h.send(Request{ID: 1, Cmd: CmdSetTun, Stack: "warp-drive"})
	r := h.await()
	if r.Ok {
		t.Fatal("set_tun accepted an unknown stack")
	}
	if !strings.Contains(r.Error, "unknown stack") {
		t.Errorf("error = %q, want it to name the unknown stack", r.Error)
	}
}

// TestSetTunIdleAppliesOnNextConnect: a stack chosen while idle is reported in
// the state and used by the next connect's config.
func TestSetTunIdleAppliesOnNextConnect(t *testing.T) {
	h := newHarness(t)
	p := seedMultiProto(t, h)

	h.send(Request{ID: 1, Cmd: CmdSetTun, Stack: singbox.StackGvisor})
	var st State
	h.dataInto(h.await(), &st)
	if st.TunStack != "gvisor" {
		t.Fatalf("tun_stack = %q, want gvisor", st.TunStack)
	}

	h.send(Request{ID: 2, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	h.awaitState(StateConnected)

	if _, stack := tunFromConfig(t, h.runner.startCfgs()[0]); stack != "gvisor" {
		t.Errorf("config stack = %q, want gvisor", stack)
	}
}

// TestSetTunLiveHotSwapsSameNode: switching the stack under a live tunnel
// restarts once on the same node with the new stack.
func TestSetTunLiveHotSwapsSameNode(t *testing.T) {
	h := newHarness(t)
	p := seedMultiProto(t, h)

	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	connected := h.awaitState(StateConnected)

	h.send(Request{ID: 2, Cmd: CmdSetTun, Stack: singbox.StackMixed})
	h.await()
	re := h.awaitState(StateConnected)
	if re["node"] != connected["node"] {
		t.Errorf("reconnected node = %v, want %v", re["node"], connected["node"])
	}

	cfgs := h.runner.startCfgs()
	if len(cfgs) != 2 {
		t.Fatalf("starts = %d, want 2", len(cfgs))
	}
	if _, stack := tunFromConfig(t, cfgs[1]); stack != "mixed" {
		t.Errorf("hot-swapped config stack = %q, want mixed", stack)
	}
	if connectedNodeTag(t, cfgs[0]) != connectedNodeTag(t, cfgs[1]) {
		t.Error("hot swap moved the selector; it must pin the current node")
	}
}

// TestSetTunSameStackDoesNotRestart: choosing the stack already in effect is a
// no-op for a live tunnel.
func TestSetTunSameStackDoesNotRestart(t *testing.T) {
	h := newHarness(t)
	p := seedMultiProto(t, h)

	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	h.awaitState(StateConnected)

	h.send(Request{ID: 2, Cmd: CmdSetTun, Stack: singbox.StackSystem})
	h.await()
	if h.runner.starts() != 1 {
		t.Errorf("starts = %d, want 1 (same stack must not restart)", h.runner.starts())
	}
}

// TestKillSwitchRelaunchesDeadTunnel: with the switch armed, an unexpected
// process exit is answered with a relaunch pinned to the same node, and the
// state comes back connected rather than error.
func TestKillSwitchRelaunchesDeadTunnel(t *testing.T) {
	h := newHarness(t)
	p := seedMultiProto(t, h)

	h.send(Request{ID: 1, Cmd: CmdSetKillSwitch, On: true})
	h.await()
	h.send(Request{ID: 2, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	connected := h.awaitState(StateConnected)

	h.runner.exit(errors.New("boom"))
	h.awaitLogContains("kill switch: tunnel process died")
	re := h.awaitState(StateConnected)
	if re["node"] != connected["node"] {
		t.Errorf("relaunched node = %v, want %v", re["node"], connected["node"])
	}

	cfgs := h.runner.startCfgs()
	if len(cfgs) != 2 {
		t.Fatalf("starts = %d, want 2 (connect + relaunch)", len(cfgs))
	}
	if strict, _ := tunFromConfig(t, cfgs[1]); !strict {
		t.Error("relaunched config lacks strict_route")
	}
	if connectedNodeTag(t, cfgs[0]) != connectedNodeTag(t, cfgs[1]) {
		t.Error("relaunch moved the selector; it must pin the node that was up")
	}
}

// TestNoRelaunchWhenDisarmed: without the kill switch, a process death remains
// a plain error state — the pre-kill-switch behaviour.
func TestNoRelaunchWhenDisarmed(t *testing.T) {
	h := newHarness(t)
	p := seedMultiProto(t, h)

	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	h.awaitState(StateConnected)

	h.runner.exit(errors.New("boom"))
	errState := h.awaitState(StateError)
	if errState["error"] != "boom" {
		t.Errorf("error = %v, want boom", errState["error"])
	}
	if h.runner.starts() != 1 {
		t.Errorf("starts = %d, want 1 (no relaunch when disarmed)", h.runner.starts())
	}
}

// TestKillSwitchRelaunchBudget: a tunnel that keeps dying gets maxRelaunches
// restarts and then an error state; an explicit reconnect opens a fresh budget.
func TestKillSwitchRelaunchBudget(t *testing.T) {
	h := newHarness(t)
	p := seedMultiProto(t, h)

	h.send(Request{ID: 1, Cmd: CmdSetKillSwitch, On: true})
	h.await()
	h.send(Request{ID: 2, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	h.awaitState(StateConnected)

	for i := 0; i < maxRelaunches; i++ {
		h.runner.exit(errors.New("boom"))
		h.awaitState(StateConnected) // each death within budget is answered
	}
	h.runner.exit(errors.New("boom"))
	h.awaitLogContains("giving up on restarts")
	h.awaitState(StateError)
	if got, want := h.runner.starts(), 1+maxRelaunches; got != want {
		t.Errorf("starts = %d, want %d (initial + budgeted relaunches)", got, want)
	}

	// A user reconnect resets the budget: the next death relaunches again.
	h.send(Request{ID: 3, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	h.awaitState(StateConnected)
	h.runner.exit(errors.New("boom"))
	h.awaitState(StateConnected)
}

// TestReapplyDefersWhenNodeVanished: if the connected node is gone from the
// store by the time a setting changes, the live tunnel is left running on the
// old options and the change is logged as deferred — never a torn-down tunnel
// with nothing to replace it.
func TestReapplyDefersWhenNodeVanished(t *testing.T) {
	h := newHarness(t)
	p := seedMultiProto(t, h)

	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	h.awaitState(StateConnected)
	// Connect's own teardown always issues one (idempotent) Stop before
	// starting; everything after this baseline must leave the runner alone.
	stopsAfterConnect := h.runner.stops()

	// Drop the connected node (the walk lands on the first, vless-id).
	p.Servers = p.Servers[1:]
	if err := h.store.Update(p); err != nil {
		t.Fatalf("update profile: %v", err)
	}

	h.send(Request{ID: 2, Cmd: CmdSetKillSwitch, On: true})
	var st State
	h.dataInto(h.await(), &st)
	if !st.KillSwitch {
		t.Fatal("kill switch not recorded when the re-apply is deferred")
	}
	h.awaitLogContains("re-apply")
	if h.runner.starts() != 1 {
		t.Errorf("starts = %d, want 1 (deferred re-apply must not restart)", h.runner.starts())
	}
	if got := h.runner.stops(); got != stopsAfterConnect {
		t.Errorf("stops = %d, want %d (the live tunnel must be left alone)", got, stopsAfterConnect)
	}
}

// TestKillSwitchAndTunPersistAcrossRestart: both preferences round-trip through
// the settings file and load into a fresh daemon's state; a corrupt stack value
// falls back to the default instead of reaching sing-box.
func TestKillSwitchAndTunPersistAcrossRestart(t *testing.T) {
	dir := t.TempDir()

	h := newHarness(t)
	st, err := OpenFileSettings(dir)
	if err != nil {
		t.Fatalf("open settings: %v", err)
	}
	h.daemon.SetSettings(st)

	h.send(Request{ID: 1, Cmd: CmdSetKillSwitch, On: true})
	h.await()
	h.send(Request{ID: 2, Cmd: CmdSetTun, Stack: singbox.StackGvisor})
	h.await()

	// A "restarted" daemon over the same directory loads both back.
	h2 := newHarness(t)
	st2, err := OpenFileSettings(dir)
	if err != nil {
		t.Fatalf("reopen settings: %v", err)
	}
	h2.daemon.SetSettings(st2)
	loaded := h2.daemon.snapshotState()
	if !loaded.KillSwitch {
		t.Error("kill switch did not survive the restart")
	}
	if loaded.TunStack != "gvisor" {
		t.Errorf("tun stack = %q, want gvisor after restart", loaded.TunStack)
	}

	// A hand-edited stack the builder doesn't know keeps the default.
	if err := st2.Save(persistedSettings{TunStack: "bogus", KillSwitch: true}); err != nil {
		t.Fatalf("save bogus settings: %v", err)
	}
	h3 := newHarness(t)
	st3, err := OpenFileSettings(dir)
	if err != nil {
		t.Fatalf("reopen settings: %v", err)
	}
	h3.daemon.SetSettings(st3)
	if got := h3.daemon.snapshotState().TunStack; got != "system" {
		t.Errorf("tun stack = %q, want the system default for a bogus persisted value", got)
	}
}
