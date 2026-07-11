package control

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Divaaaan/tenebra/core/profile"
)

// These tests cover the forced TLS-fragmentation toggle end to end at the protocol
// level: recording and reporting the choice, folding tls.fragment into the built
// config, hot-swapping a live tunnel in place (same node, no fallback walk),
// applying a mid-connect toggle to the tunnel that comes up, and persisting the
// preference across a daemon restart — the same contract the kill switch holds.

// selectedOutboundFragment reports whether the outbound the config's selector
// defaults to carries tls.fragment, so a test can assert the toggle reached the
// node that actually came up.
func selectedOutboundFragment(t *testing.T, cfgJSON []byte) bool {
	t.Helper()
	var cfg struct {
		Outbounds []map[string]any `json:"outbounds"`
	}
	if err := json.Unmarshal(cfgJSON, &cfg); err != nil {
		t.Fatalf("parse config: %v", err)
	}
	def := ""
	for _, o := range cfg.Outbounds {
		if o["type"] == "selector" {
			def, _ = o["default"].(string)
		}
	}
	for _, o := range cfg.Outbounds {
		if o["tag"] != def {
			continue
		}
		tls, _ := o["tls"].(map[string]any)
		frag, _ := tls["fragment"].(bool)
		return frag
	}
	t.Fatalf("no outbound tagged %q in config", def)
	return false
}

// seedFragmentProfile stores a one-node profile whose node carries a TLS block, so
// the fragmentation toggle has a ClientHello to fragment and the assertions can
// read tls.fragment off the built config.
func seedFragmentProfile(t *testing.T, h *harness) profile.Profile {
	t.Helper()
	p := profile.Profile{
		ID: "frag", Name: "Frag", Source: profile.SourceManual,
		Servers: []profile.Server{vlessRealityServer("vless-id", "Reality-FRA", "chrome")},
	}
	if err := h.store.Add(p); err != nil {
		t.Fatalf("add fragment profile: %v", err)
	}
	return p
}

// TestSetTLSFragmentIdleAppliesOnNextConnect: arming while idle is recorded,
// reported, and lands as tls.fragment in the next connect's config.
func TestSetTLSFragmentIdleAppliesOnNextConnect(t *testing.T) {
	h := newHarness(t)
	p := seedFragmentProfile(t, h)

	h.send(Request{ID: 1, Cmd: CmdSetTLSFragment, On: true})
	var st State
	h.dataInto(h.await(), &st)
	if !st.TLSFragment {
		t.Fatalf("set_tls_fragment response tls_fragment = false, want true")
	}
	if h.runner.starts() != 0 {
		t.Fatalf("arming while idle started a process (starts=%d)", h.runner.starts())
	}

	h.send(Request{ID: 2, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	h.awaitState(StateConnected)

	if !selectedOutboundFragment(t, h.runner.startCfgs()[0]) {
		t.Error("config built with tls-fragment armed lacks tls.fragment on the selected node")
	}
}

// TestTLSFragmentDefaultsOff: without arming, the built config must not carry
// tls.fragment — fragmentation is strictly opt-in.
func TestTLSFragmentDefaultsOff(t *testing.T) {
	h := newHarness(t)
	p := seedFragmentProfile(t, h)

	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	h.awaitState(StateConnected)

	if selectedOutboundFragment(t, h.runner.startCfgs()[0]) {
		t.Error("tls.fragment on by default; the toggle must be opt-in")
	}
}

// TestSetTLSFragmentLiveHotSwapsSameNode: arming while connected restarts sing-box
// once, pinned to the node already in use, and comes back connected with
// tls.fragment in the new config.
func TestSetTLSFragmentLiveHotSwapsSameNode(t *testing.T) {
	h := newHarness(t)
	p := seedFragmentProfile(t, h)

	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	connected := h.awaitState(StateConnected)
	node := connected["node"].(string)

	h.send(Request{ID: 2, Cmd: CmdSetTLSFragment, On: true})
	var st State
	h.dataInto(h.await(), &st)
	if !st.TLSFragment {
		t.Fatal("response tls_fragment = false, want true")
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
	if selectedOutboundFragment(t, cfgs[0]) {
		t.Error("initial config already carried tls.fragment")
	}
	if !selectedOutboundFragment(t, cfgs[1]) {
		t.Error("hot-swapped config lacks tls.fragment")
	}
	if tagBefore, tagAfter := connectedNodeTag(t, cfgs[0]), connectedNodeTag(t, cfgs[1]); tagBefore != tagAfter {
		t.Errorf("hot swap moved the selector: %q -> %q; it must pin the current node", tagBefore, tagAfter)
	}
}

// TestSetTLSFragmentUnchangedDoesNotRestart: re-sending the current value must not
// bounce a live tunnel.
func TestSetTLSFragmentUnchangedDoesNotRestart(t *testing.T) {
	h := newHarness(t)
	p := seedFragmentProfile(t, h)

	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	h.awaitState(StateConnected)

	h.send(Request{ID: 2, Cmd: CmdSetTLSFragment, On: false}) // already off
	h.await()
	if h.runner.starts() != 1 {
		t.Errorf("starts = %d, want 1 (no-op toggle must not restart)", h.runner.starts())
	}
}

// TestSetTLSFragmentPersistsAcrossRestart: the toggle round-trips through the
// settings file and loads into a fresh daemon's state.
func TestSetTLSFragmentPersistsAcrossRestart(t *testing.T) {
	dir := t.TempDir()

	h := newHarness(t)
	st, err := OpenFileSettings(dir)
	if err != nil {
		t.Fatalf("open settings: %v", err)
	}
	h.daemon.SetSettings(st)

	h.send(Request{ID: 1, Cmd: CmdSetTLSFragment, On: true})
	h.await()

	// A "restarted" daemon over the same directory loads it back.
	h2 := newHarness(t)
	st2, err := OpenFileSettings(dir)
	if err != nil {
		t.Fatalf("reopen settings: %v", err)
	}
	h2.daemon.SetSettings(st2)
	if !h2.daemon.snapshotState().TLSFragment {
		t.Error("tls-fragment did not survive the restart")
	}
}

// TestSetTLSFragmentDuringConnectingReconciles: a set_tls_fragment that lands while
// a connect is still in its warmup+probe window is applied to the tunnel that
// comes up, not merely recorded. The fallback loop pins its options snapshot at
// the start of the connect, so without the connecting-window reconcile the live
// tunnel would run without fragmentation while the state reports it armed. The
// reconcile hot-swaps once the connect settles, so the final live config carries
// tls.fragment.
func TestSetTLSFragmentDuringConnectingReconciles(t *testing.T) {
	h := newHarness(t)
	p := seedFragmentProfile(t, h)

	// Park the first connect inside its probe so the toggle below is guaranteed to
	// land while the state is still "connecting" (its config already built without
	// fragmentation). Later probes — including the reconcile's — run unblocked.
	release := make(chan struct{})
	probing := make(chan struct{}, 1)
	h.runner.onProbe = func(_ context.Context, n int) {
		if n != 1 {
			return
		}
		select {
		case probing <- struct{}{}:
		default:
		}
		<-release
	}

	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	h.awaitState(StateConnecting)
	<-probing // first connect is inside its probe; config[0] is built (no fragment)

	h.send(Request{ID: 2, Cmd: CmdSetTLSFragment, On: true})
	var armed State
	h.dataInto(h.await(), &armed)
	if !armed.TLSFragment {
		t.Fatal("set_tls_fragment response tls_fragment = false, want true")
	}

	close(release) // let the first connect finish; the reconcile then hot-swaps

	h.waitStarts(2)
	cfgs := h.runner.startCfgs()
	if selectedOutboundFragment(t, cfgs[0]) {
		t.Error("first (connecting-window) config already carried tls.fragment")
	}
	if !selectedOutboundFragment(t, cfgs[len(cfgs)-1]) {
		t.Error("reconciled config lacks tls.fragment; the mid-connect toggle was not applied to the live tunnel")
	}
	if connectedNodeTag(t, cfgs[0]) != connectedNodeTag(t, cfgs[len(cfgs)-1]) {
		t.Error("reconcile moved the selector; it must pin the node that came up")
	}

	waitFor(t, func() bool {
		st := h.daemon.snapshotState()
		return st.State == StateConnected && st.TLSFragment && h.runner.starts() == 2
	}, "reconcile to settle connected and fragmented")
}
