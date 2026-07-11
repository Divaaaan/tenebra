package control

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"syscall"
	"testing"

	"github.com/Divaaaan/tenebra/core/fallback"
	"github.com/Divaaaan/tenebra/core/model"
	"github.com/Divaaaan/tenebra/core/profile"
	"github.com/Divaaaan/tenebra/core/routing"
	"github.com/Divaaaan/tenebra/core/singbox"
)

// These tests cover the adaptive-transport escalation the fallback loop layers on
// top of the node walk: a node whose handshake looks interfered with (the
// classifier's Censored verdict) is re-tried under the next transport strategy on
// the SAME node before the walk moves on, while a dead or ambiguous node advances
// straight to the next node. The classify seam is scripted so the loop's decision
// is exercised without a network; a separate pair of tests pins the production
// classifier's own signal mapping.

// vlessRealityServer builds a VLESS+REALITY server carrying an explicit uTLS
// fingerprint, so a test can watch a transport strategy reshape it across
// attempts (chrome on the native try, firefox once escalated).
func vlessRealityServer(id, name, fingerprint string) profile.Server {
	return profile.Server{ID: id, Node: model.Node{
		Protocol: model.VLESS,
		Name:     name,
		Server:   name + ".example.com",
		Port:     443,
		UUID:     "11111111-1111-1111-1111-111111111111",
		TLS: &model.TLS{
			Enabled:     true,
			ServerName:  "www.example.org",
			Fingerprint: fingerprint,
			Reality:     &model.Reality{PublicKey: "cHVibGlja2V5", ShortID: "00"},
		},
	}}
}

// selectedOutboundTLS returns the uTLS fingerprint and SNI of the outbound the
// config's selector defaults to, so a test can assert which transport strategy a
// given attempt was built with.
func selectedOutboundTLS(t *testing.T, cfgJSON []byte) (fingerprint, serverName string) {
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
		if tls == nil {
			return "", ""
		}
		if sn, ok := tls["server_name"].(string); ok {
			serverName = sn
		}
		if utls, ok := tls["utls"].(map[string]any); ok {
			if f, ok := utls["fingerprint"].(string); ok {
				fingerprint = f
			}
		}
		return fingerprint, serverName
	}
	t.Fatalf("no outbound tagged %q in config", def)
	return "", ""
}

// itemFor returns the snapshot's item for node, failing if absent.
func itemFor(t *testing.T, snap attemptsEvent, node string) attemptItem {
	t.Helper()
	for _, it := range snap.Items {
		if it.Node == node {
			return it
		}
	}
	t.Fatalf("no item for node %q in %+v", node, snap)
	return attemptItem{}
}

// TestAdaptiveEscalatesStrategyOnCensoredNode: a node whose native (chrome)
// handshake is classified censored is re-tried on the SAME node under the next
// transport strategy (firefox fingerprint), which comes up. The walk must start
// two processes for the one node, the second carrying the reshaped fingerprint,
// and record the winning strategy in the snapshot.
func TestAdaptiveEscalatesStrategyOnCensoredNode(t *testing.T) {
	h := newHarness(t)
	p := profile.Profile{ID: "adapt", Name: "Adapt", Source: profile.SourceManual,
		Servers: []profile.Server{vlessRealityServer("vless-id", "Reality-1", "chrome")}}
	if err := h.store.Add(p); err != nil {
		t.Fatalf("add profile: %v", err)
	}
	// Every failure looks like interference, so the node escalates its strategy.
	h.daemon.classify = func(context.Context, model.Node, bool) fallback.FailureClass { return fallback.Censored }
	h.runner.failStarts = 1 // native (default) strategy blocked; firefox-fp comes up

	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID})
	h.await()

	seq := h.awaitAttemptsWalk()
	last := seq[len(seq)-1]
	if last.Outcome != AttemptOutcomeConnected {
		t.Fatalf("terminal outcome = %q, want ok", last.Outcome)
	}
	ok := itemFor(t, last, "vless-id")
	if ok.Status != AttemptOK {
		t.Fatalf("node status = %q, want ok (same node, escalated strategy)", ok.Status)
	}
	if ok.Strategy != "firefox-fp" {
		t.Errorf("connected strategy = %q, want firefox-fp", ok.Strategy)
	}

	if h.runner.starts() != 2 {
		t.Fatalf("starts = %d, want 2 (native blocked, firefox-fp up on the same node)", h.runner.starts())
	}
	cfgs := h.runner.startCfgs()
	// The first attempt is the node's native chrome fingerprint; the second is the
	// escalated firefox reshaping — same node, different handshake.
	if fp, _ := selectedOutboundTLS(t, cfgs[0]); fp != "chrome" {
		t.Errorf("first attempt fingerprint = %q, want chrome (native)", fp)
	}
	if fp, _ := selectedOutboundTLS(t, cfgs[1]); fp != "firefox" {
		t.Errorf("second attempt fingerprint = %q, want firefox (escalated)", fp)
	}
}

// TestAdaptiveDeadNodeAdvancesToNextNode: a node classified dead is abandoned for
// the next node immediately, with no strategy escalation — proven by the walk
// landing on the second node rather than reviving the first under a reshaped
// handshake.
func TestAdaptiveDeadNodeAdvancesToNextNode(t *testing.T) {
	h := newHarness(t)
	p := profile.Profile{ID: "adapt", Name: "Adapt", Source: profile.SourceManual,
		Servers: []profile.Server{
			vlessRealityServer("vless-id", "Reality-1", "chrome"),
			srv("hy2-id", model.Hysteria2, "Hysteria-1"),
		}}
	if err := h.store.Add(p); err != nil {
		t.Fatalf("add profile: %v", err)
	}
	// A dead verdict must jump straight to the next node. If it wrongly escalated,
	// the first node's firefox-fp attempt (start 2) would come up and connect on
	// vless-id instead.
	h.daemon.classify = func(context.Context, model.Node, bool) fallback.FailureClass { return fallback.Dead }
	h.runner.failStarts = 1 // first node blocked; the next node comes up

	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID})
	h.await()

	seq := h.awaitAttemptsWalk()
	last := seq[len(seq)-1]
	ok := itemFor(t, last, "hy2-id")
	if ok.Status != AttemptOK {
		t.Fatalf("did not connect on the second node; hy2 status = %q, want ok", ok.Status)
	}
	// The dead first node carries no censored reason (an ordinary block).
	if r := itemFor(t, last, "vless-id").Reason; r != "" {
		t.Errorf("dead node reason = %q, want empty", r)
	}
	if h.runner.starts() != 2 {
		t.Errorf("starts = %d, want 2 (one per node, no strategy escalation)", h.runner.starts())
	}
}

// TestAdaptiveExhaustedStrategiesAdvanceToNextNode: a node that stays censored
// through its whole transport cascade is finally abandoned for the next node, and
// its block is annotated with the censored reason. It must start one process per
// strategy on the censored node before advancing.
func TestAdaptiveExhaustedStrategiesAdvanceToNextNode(t *testing.T) {
	h := newHarness(t)
	p := profile.Profile{ID: "adapt", Name: "Adapt", Source: profile.SourceManual,
		Servers: []profile.Server{
			vlessRealityServer("vless-id", "Reality-1", "chrome"),
			srv("hy2-id", model.Hysteria2, "Hysteria-1"),
		}}
	if err := h.store.Add(p); err != nil {
		t.Fatalf("add profile: %v", err)
	}
	nStrat := len(fallback.DefaultStrategies)
	h.daemon.classify = func(context.Context, model.Node, bool) fallback.FailureClass { return fallback.Censored }
	// Block every strategy attempt on the first node; the next node's first attempt
	// (start nStrat+1) comes up.
	h.runner.failStarts = nStrat

	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID})
	h.await()

	seq := h.awaitAttemptsWalk()
	last := seq[len(seq)-1]
	if got := itemFor(t, last, "hy2-id").Status; got != AttemptOK {
		t.Fatalf("did not connect on the second node; hy2 status = %q, want ok", got)
	}
	blocked := itemFor(t, last, "vless-id")
	if blocked.Status != AttemptBlocked {
		t.Errorf("exhausted node status = %q, want blocked", blocked.Status)
	}
	if blocked.Reason != fallback.Censored.String() {
		t.Errorf("exhausted node reason = %q, want censored", blocked.Reason)
	}
	// One process per strategy on the censored node, then one for the node that came up.
	if want := nStrat + 1; h.runner.starts() != want {
		t.Errorf("starts = %d, want %d (%d strategies on the censored node + 1 next node)", h.runner.starts(), want, nStrat)
	}
}

// TestApplyStrategyRendersIntoConfig ties the strategy model to the builder: the
// most divergent strategy reshapes both the fingerprint and the SNI of the
// selected node, and those land in the generated config's outbound — while the
// shared input node is left untouched, the property the connect loop relies on to
// reuse one node slice across every attempt.
func TestApplyStrategyRendersIntoConfig(t *testing.T) {
	nodes := []model.Node{vlessRealityServer("vless-id", "Reality-1", "chrome").Node}
	nodeIDs := []string{"vless-id"}
	strat := fallback.Strategy{Name: "alt-sni", Fingerprint: "firefox", ServerName: "static.example"}

	out := applyStrategyToNodes(nodes, nodeIDs, "vless-id", strat)
	cfgJSON, err := buildConfigJSON(out, "Reality-1", routing.Options{Mode: routing.ModeSmart}, singbox.TunOptions{})
	if err != nil {
		t.Fatalf("build config: %v", err)
	}
	fp, sni := selectedOutboundTLS(t, cfgJSON)
	if fp != "firefox" {
		t.Errorf("rendered fingerprint = %q, want firefox", fp)
	}
	if sni != "static.example" {
		t.Errorf("rendered server_name = %q, want static.example", sni)
	}
	// The input slice's node keeps its native parameters — ApplyTo copied the TLS.
	if nodes[0].TLS.Fingerprint != "chrome" || nodes[0].TLS.ServerName != "www.example.org" {
		t.Errorf("input node was mutated: %+v", nodes[0].TLS)
	}
}

// TestClassifyAttemptFailure exercises the production classifier end to end: it
// dials the entry through the injectable dialer and pairs the observed TCP result
// with the handshake-stall signal to reach a verdict. This is the wiring the
// escalation tests stub out, so the two together cover both the decision and the
// signal it rests on.
func TestClassifyAttemptFailure(t *testing.T) {
	node := model.Node{Server: "entry.example", Port: 443}

	dialOK := func(context.Context, string, string) (net.Conn, error) { return fakeConn{}, nil }
	dialErr := func(err error) func(context.Context, string, string) (net.Conn, error) {
		return func(context.Context, string, string) (net.Conn, error) { return nil, err }
	}

	tests := []struct {
		name    string
		dial    func(context.Context, string, string) (net.Conn, error)
		stalled bool
		want    fallback.FailureClass
	}{
		{"reachable entry, silent stall is censored", dialOK, true, fallback.Censored},
		{"reachable entry, fast handshake error is unknown", dialOK, false, fallback.Unknown},
		{"refused entry is dead", dialErr(syscall.ECONNREFUSED), true, fallback.Dead},
		{"unreachable entry is dead", dialErr(syscall.EHOSTUNREACH), true, fallback.Dead},
		{"timed-out entry is unknown", dialErr(context.DeadlineExceeded), true, fallback.Unknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, _ := newTestDaemon(t)
			d.dial = tt.dial
			if got := d.classifyAttemptFailure(context.Background(), node, tt.stalled); got != tt.want {
				t.Errorf("classifyAttemptFailure = %v, want %v", got, tt.want)
			}
		})
	}

	// A node missing an address can't be probed, so it stays indeterminate — which
	// with a stall is not enough to call censored.
	t.Run("no address is not censored", func(t *testing.T) {
		d, _ := newTestDaemon(t)
		d.dial = func(context.Context, string, string) (net.Conn, error) {
			t.Fatal("dial should not run for an address-less node")
			return nil, nil
		}
		if got := d.classifyAttemptFailure(context.Background(), model.Node{}, true); got != fallback.Unknown {
			t.Errorf("verdict = %v, want unknown", got)
		}
	})
}

// netTimeoutErr is a net.Error reporting a timeout, for the dial-error mapping.
type netTimeoutErr struct{}

func (netTimeoutErr) Error() string   { return "i/o timeout" }
func (netTimeoutErr) Timeout() bool   { return true }
func (netTimeoutErr) Temporary() bool { return true }

// TestClassifyDialError pins how a failed TCP dial's error maps to a TCPResult:
// a reset is a refused entry, an unroutable/unresolvable address is unreachable,
// a deadline or net timeout is a no-answer timeout, and anything else stays
// indeterminate rather than being forced into a verdict.
func TestClassifyDialError(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		ctxErr error
		want   fallback.TCPResult
	}{
		{"refused", syscall.ECONNREFUSED, nil, fallback.TCPRefused},
		{"wrapped refused", &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}, nil, fallback.TCPRefused},
		{"host unreachable", syscall.EHOSTUNREACH, nil, fallback.TCPUnreachable},
		{"net unreachable", syscall.ENETUNREACH, nil, fallback.TCPUnreachable},
		{"dns failure", &net.DNSError{Err: "no such host", Name: "x.example"}, nil, fallback.TCPUnreachable},
		{"deadline error", context.DeadlineExceeded, context.DeadlineExceeded, fallback.TCPTimedOut},
		{"deadline via ctx only", errors.New("some dial error"), context.DeadlineExceeded, fallback.TCPTimedOut},
		{"net timeout", netTimeoutErr{}, nil, fallback.TCPTimedOut},
		{"unrecognised", errors.New("boom"), nil, fallback.TCPIndeterminate},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyDialError(tt.err, tt.ctxErr); got != tt.want {
				t.Errorf("classifyDialError(%v, %v) = %v, want %v", tt.err, tt.ctxErr, got, tt.want)
			}
		})
	}
}
