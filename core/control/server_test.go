package control

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/Divaaaan/tenebra/core/fallback"
	"github.com/Divaaaan/tenebra/core/model"
	"github.com/Divaaaan/tenebra/core/profile"
)

// harness drives a Server over two pipes with a fake runner, demultiplexing the
// output stream into responses and events.
type harness struct {
	t      *testing.T
	daemon *Daemon
	runner *fakeRunner
	store  *profile.Store

	inW    io.WriteCloser
	cancel context.CancelFunc
	done   chan error

	resp   chan Response
	events chan map[string]any
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	store, err := profile.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	runner := newFakeRunner()
	d := NewDaemon(store, runner)
	// Shrink the fallback-loop timings so tests don't wait out real warmups/budgets.
	// The fake runner's Probe answers instantly, so a blocked candidate must burn
	// its whole (tiny) budget before the loop gives up on it — keep the budget
	// short so a multi-candidate walk finishes well inside the await deadlines.
	d.probeWarmup = time.Millisecond
	d.probeRetry = time.Millisecond
	d.probeTimeout = 200 * time.Millisecond
	d.probeBudget = 60 * time.Millisecond
	// Adaptive escalation is off by default in the harness: the fake runner's
	// nodes aren't real, so a live classify would dial the network on every
	// blocked candidate. Tests that exercise escalation override d.classify.
	d.classify = func(context.Context, model.Node, bool) fallback.FailureClass { return fallback.Unknown }

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	srv := NewServer(d, inR, outW)

	ctx, cancel := context.WithCancel(context.Background())
	h := &harness{
		t:      t,
		daemon: d,
		runner: runner,
		store:  store,
		inW:    inW,
		cancel: cancel,
		done:   make(chan error, 1),
		resp:   make(chan Response, 64),
		events: make(chan map[string]any, 256),
	}

	go func() { h.done <- srv.Serve(ctx) }()
	go h.readLoop(outR)

	t.Cleanup(func() {
		cancel()
		inW.Close()
	})
	return h
}

// readLoop classifies each output line as a response (has "ok") or an event (has
// "event") and routes it to the matching channel.
func (h *harness) readLoop(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var probe map[string]any
		if err := json.Unmarshal(line, &probe); err != nil {
			continue
		}
		if _, isEvent := probe["event"]; isEvent {
			cp := make(map[string]any, len(probe))
			for k, v := range probe {
				cp[k] = v
			}
			// Best-effort: drop events when the buffer is full rather than block the
			// pipe reader. A live connection's traffic poller keeps emitting every
			// second, so once a test stops draining events the reader must not wedge —
			// otherwise the daemon's emit (a pipe write) blocks and Close()'s wg.Wait
			// deadlocks at cleanup. Tests assert on the events they care about well
			// before the 256-deep buffer fills.
			select {
			case h.events <- cp:
			default:
			}
			continue
		}
		var resp Response
		if err := json.Unmarshal(line, &resp); err == nil {
			h.resp <- resp
		}
	}
}

// send writes a request line.
func (h *harness) send(req Request) {
	h.t.Helper()
	line, err := marshalLine(req)
	if err != nil {
		h.t.Fatalf("marshal request: %v", err)
	}
	if _, err := h.inW.Write(line); err != nil {
		h.t.Fatalf("write request: %v", err)
	}
}

// await reads the next response, failing on timeout.
func (h *harness) await() Response {
	h.t.Helper()
	select {
	case r := <-h.resp:
		return r
	case <-time.After(3 * time.Second):
		h.t.Fatal("timed out waiting for response")
		return Response{}
	}
}

// awaitState waits for a state event whose state equals want, returning it. It
// drains intervening events (e.g. traffic).
func (h *harness) awaitState(want ConnState) map[string]any {
	h.t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case ev := <-h.events:
			if ev["event"] == EventState && ev["state"] == string(want) {
				return ev
			}
		case <-deadline:
			h.t.Fatalf("timed out waiting for state=%s event", want)
			return nil
		}
	}
}

// awaitEvent waits for the next event with the given name.
func (h *harness) awaitEvent(name string) map[string]any {
	h.t.Helper()
	deadline := time.After(4 * time.Second)
	for {
		select {
		case ev := <-h.events:
			if ev["event"] == name {
				return ev
			}
		case <-deadline:
			h.t.Fatalf("timed out waiting for %s event", name)
			return nil
		}
	}
}

// waitStarts blocks until the fake runner has been started at least n times,
// failing on timeout. connect now starts sing-box from a background loop, so a
// test that needs the process to exist must wait for the Start rather than assume
// it happened by the time the connecting response arrived.
func (h *harness) waitStarts(n int) {
	h.t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		if h.runner.starts() >= n {
			return
		}
		select {
		case <-deadline:
			h.t.Fatalf("timed out waiting for %d runner start(s); got %d", n, h.runner.starts())
		case <-time.After(2 * time.Millisecond):
		}
	}
}

// dataInto decodes a response's data payload into v.
func (h *harness) dataInto(r Response, v any) {
	h.t.Helper()
	if !r.Ok {
		h.t.Fatalf("response not ok: %s", r.Error)
	}
	if err := json.Unmarshal(r.Data, v); err != nil {
		h.t.Fatalf("decode data: %v", err)
	}
}

func fakeVLESSLink() string {
	// Obviously fake: example host, all-ones UUID.
	return "vless://11111111-1111-1111-1111-111111111111@a.example.com:443?security=reality&pbk=PUBKEY&fp=chrome&sni=example.com&flow=xtls-rprx-vision#Frankfurt"
}

// importFakeProfile imports a manual one-node profile via the link command and
// returns the created profile.
func (h *harness) importFakeProfile() profile.Profile {
	h.t.Helper()
	h.send(Request{ID: 1, Cmd: CmdImportLink, Link: fakeVLESSLink(), Name: "Test"})
	r := h.await()
	var out struct {
		Profile profile.Profile `json:"profile"`
	}
	h.dataInto(r, &out)
	return out.Profile
}

// addProfile builds a profile from nodes and writes it straight to the store,
// bypassing the import path so a test can stand up a multi-node profile with
// controlled server addresses (the import path only yields one node per link).
func (h *harness) addProfile(nodes []model.Node) profile.Profile {
	h.t.Helper()
	p, err := profile.NewProfile("Multi", profile.SourceManual, "", nodes)
	if err != nil {
		h.t.Fatalf("new profile: %v", err)
	}
	if err := h.store.Add(p); err != nil {
		h.t.Fatalf("add profile: %v", err)
	}
	return p
}

// vlessNode is a minimal renderable VLESS node distinguished only by server
// address — enough for buildCandidates/serverTags and for a per-host dialer to
// assign it a latency. Each gets a unique UUID so their stable IDs differ.
func vlessNode(name, server string) model.Node {
	return model.Node{
		Protocol: model.VLESS,
		Name:     name,
		Server:   server,
		Port:     443,
		UUID:     "11111111-1111-1111-1111-1111111111" + server[len(server)-2:],
		Flow:     "xtls-rprx-vision",
		TLS: &model.TLS{
			Enabled:    true,
			ServerName: "example.com",
			Reality:    &model.Reality{PublicKey: "PUBKEY"},
		},
	}
}

func TestStatusInitiallyIdle(t *testing.T) {
	h := newHarness(t)
	h.send(Request{ID: 1, Cmd: CmdStatus})
	r := h.await()
	if r.ID != 1 {
		t.Errorf("response id = %d, want 1", r.ID)
	}
	var st State
	h.dataInto(r, &st)
	if st.State != StateIdle {
		t.Errorf("initial state = %q, want idle", st.State)
	}
	if st.Routing != string(model.Protocol("smart")) && st.Routing != "smart" {
		t.Errorf("initial routing = %q, want smart", st.Routing)
	}
}

func TestUnknownCommand(t *testing.T) {
	h := newHarness(t)
	h.send(Request{ID: 5, Cmd: "nonsense"})
	r := h.await()
	if r.Ok {
		t.Fatal("unknown command should fail")
	}
	if r.ID != 5 {
		t.Errorf("error response id = %d, want 5", r.ID)
	}
}

func TestMalformedLineGetsErrorResponse(t *testing.T) {
	h := newHarness(t)
	if _, err := h.inW.Write([]byte("{not json}\n")); err != nil {
		t.Fatal(err)
	}
	r := h.await()
	if r.Ok {
		t.Error("malformed line should produce an error response")
	}
}

func TestImportLinkThenList(t *testing.T) {
	h := newHarness(t)
	p := h.importFakeProfile()
	if p.Source != profile.SourceManual {
		t.Errorf("source = %q, want manual", p.Source)
	}
	if len(p.Servers) != 1 {
		t.Fatalf("imported %d servers, want 1", len(p.Servers))
	}
	if p.Servers[0].Protocol != model.VLESS {
		t.Errorf("protocol = %q, want vless", p.Servers[0].Protocol)
	}
	// URL must not be echoed for a manual import.
	if p.URL != "" {
		t.Errorf("manual profile carries url %q", p.URL)
	}

	h.send(Request{ID: 2, Cmd: CmdListProfiles})
	r := h.await()
	var out struct {
		Profiles []profile.Profile `json:"profiles"`
	}
	h.dataInto(r, &out)
	if len(out.Profiles) != 1 || out.Profiles[0].ID != p.ID {
		t.Errorf("list = %+v, want one profile %s", out.Profiles, p.ID)
	}
}

func TestImportLinkBadLink(t *testing.T) {
	h := newHarness(t)
	h.send(Request{ID: 1, Cmd: CmdImportLink, Link: "not-a-link"})
	r := h.await()
	if r.Ok {
		t.Error("bad link should fail")
	}
}

func TestImportLinksBatchOneProfileManyServers(t *testing.T) {
	h := newHarness(t)
	// A pasted block: two good links of different protocols, one comment, one
	// blank line, and one junk line that must be skipped (not fatal).
	block := strings.Join([]string{
		"# my nodes",
		fakeVLESSLink(),
		"",
		"trojan://pw@b.example.com:443#Berlin",
		"not-a-link",
	}, "\n")

	h.send(Request{ID: 1, Cmd: CmdImportLinks, Links: []string{block}, Name: "Batch"})
	r := h.await()
	var out struct {
		Profile  profile.Profile `json:"profile"`
		Imported int             `json:"imported"`
		Skipped  int             `json:"skipped"`
	}
	h.dataInto(r, &out)

	if out.Imported != 2 || out.Skipped != 1 {
		t.Errorf("imported/skipped = %d/%d, want 2/1", out.Imported, out.Skipped)
	}
	if out.Profile.Source != profile.SourceManual {
		t.Errorf("source = %q, want manual", out.Profile.Source)
	}
	if len(out.Profile.Servers) != 2 {
		t.Fatalf("profile has %d servers, want 2 (one profile, many servers)", len(out.Profile.Servers))
	}
	if out.Profile.Name != "Batch" {
		t.Errorf("name = %q, want Batch", out.Profile.Name)
	}
	// The profile must actually be in the store as a single entry.
	h.send(Request{ID: 2, Cmd: CmdListProfiles})
	r = h.await()
	var list struct {
		Profiles []profile.Profile `json:"profiles"`
	}
	h.dataInto(r, &list)
	if len(list.Profiles) != 1 || list.Profiles[0].ID != out.Profile.ID {
		t.Errorf("list = %+v, want one profile %s", list.Profiles, out.Profile.ID)
	}
}

func TestImportLinksDefaultsNameAndDedupes(t *testing.T) {
	h := newHarness(t)
	// Same link twice -> one server. No name given -> a sensible default.
	h.send(Request{ID: 1, Cmd: CmdImportLinks, Links: []string{fakeVLESSLink(), fakeVLESSLink()}})
	r := h.await()
	var out struct {
		Profile  profile.Profile `json:"profile"`
		Imported int             `json:"imported"`
		Skipped  int             `json:"skipped"`
	}
	h.dataInto(r, &out)
	if out.Imported != 1 || out.Skipped != 0 {
		t.Errorf("imported/skipped = %d/%d, want 1/0 (deduped)", out.Imported, out.Skipped)
	}
	if out.Profile.Name == "" {
		t.Error("expected a default profile name, got empty")
	}
}

func TestImportLinksAllInvalidFails(t *testing.T) {
	h := newHarness(t)
	h.send(Request{ID: 1, Cmd: CmdImportLinks, Links: []string{"nope", "also-bad"}})
	r := h.await()
	if r.Ok {
		t.Error("a batch with no valid links should fail rather than create an empty profile")
	}
}

func TestImportLinksMissingFails(t *testing.T) {
	h := newHarness(t)
	h.send(Request{ID: 1, Cmd: CmdImportLinks, Links: nil})
	r := h.await()
	if r.Ok {
		t.Error("missing links should fail")
	}
}

func TestListProfilesEmptyIsArray(t *testing.T) {
	h := newHarness(t)
	h.send(Request{ID: 1, Cmd: CmdListProfiles})
	r := h.await()
	// The data payload's profiles must serialise as [], not null, so the UI can
	// iterate it unconditionally.
	var probe struct {
		Profiles json.RawMessage `json:"profiles"`
	}
	h.dataInto(r, &probe)
	if string(probe.Profiles) != "[]" {
		t.Errorf("empty profiles = %s, want []", probe.Profiles)
	}
}

func TestRemoveProfile(t *testing.T) {
	h := newHarness(t)
	p := h.importFakeProfile()
	h.send(Request{ID: 2, Cmd: CmdRemoveProfile, Profile: p.ID})
	r := h.await()
	if !r.Ok {
		t.Fatalf("remove failed: %s", r.Error)
	}
	if len(h.store.List()) != 0 {
		t.Error("profile still in store after remove")
	}
}

func TestRemoveMissingProfile(t *testing.T) {
	h := newHarness(t)
	h.send(Request{ID: 1, Cmd: CmdRemoveProfile, Profile: "ghost"})
	r := h.await()
	if r.Ok {
		t.Error("removing a missing profile should fail")
	}
}

func TestSetRouting(t *testing.T) {
	h := newHarness(t)
	h.send(Request{ID: 1, Cmd: CmdSetRouting, Mode: "global"})
	r := h.await()
	var st State
	h.dataInto(r, &st)
	if st.Routing != "global" {
		t.Errorf("routing = %q, want global", st.Routing)
	}

	// Status must reflect the new mode.
	h.send(Request{ID: 2, Cmd: CmdStatus})
	r2 := h.await()
	var st2 State
	h.dataInto(r2, &st2)
	if st2.Routing != "global" {
		t.Errorf("status routing = %q, want global", st2.Routing)
	}
}

func TestSetRoutingBadMode(t *testing.T) {
	h := newHarness(t)
	h.send(Request{ID: 1, Cmd: CmdSetRouting, Mode: "sideways"})
	r := h.await()
	if r.Ok {
		t.Error("bad routing mode should fail")
	}
}

// TestConnectDisconnectDriveState is the core lifecycle test: connect moves the
// state machine idle->connecting->connected (a state event for each non-idle
// transition), the runner is actually started with a config, and disconnect
// stops it and returns to idle.
func TestConnectDisconnectDriveState(t *testing.T) {
	h := newHarness(t)
	p := h.importFakeProfile()

	h.send(Request{ID: 2, Cmd: CmdConnect, Profile: p.ID})
	r := h.await()
	var st State
	h.dataInto(r, &st)
	if st.State != StateConnecting {
		t.Errorf("connect response state = %q, want connecting", st.State)
	}
	// The connect response carries the profile but not yet a node: with protocol
	// fallback the node is only known once a candidate's probe succeeds, and it
	// arrives on the connected event below.
	if st.Profile != p.ID {
		t.Errorf("connect response profile = %q, want %s", st.Profile, p.ID)
	}

	// The runner is started from the background loop; wait for it, then check the
	// config it was handed.
	h.waitStarts(1)
	if len(h.runner.lastCfg) == 0 {
		t.Error("runner started with empty config")
	}
	// And the config must be valid sing-box-ish JSON with outbounds.
	var cfg map[string]any
	if err := json.Unmarshal(h.runner.lastCfg, &cfg); err != nil {
		t.Fatalf("runner config is not valid json: %v", err)
	}
	if _, ok := cfg["outbounds"]; !ok {
		t.Error("config has no outbounds")
	}

	// A connecting state event, then (the probe confirms the tunnel) a connected
	// one carrying the node the fallback loop landed on.
	h.awaitState(StateConnecting)
	connected := h.awaitState(StateConnected)
	if connected["node"] != p.Servers[0].ID {
		t.Errorf("connected event node = %v, want %s", connected["node"], p.Servers[0].ID)
	}

	h.send(Request{ID: 3, Cmd: CmdDisconnect})
	r3 := h.await()
	var st3 State
	h.dataInto(r3, &st3)
	if st3.State != StateIdle {
		t.Errorf("disconnect state = %q, want idle", st3.State)
	}
	if h.runner.stops() < 1 {
		t.Error("runner not stopped on disconnect")
	}
	h.awaitState(StateIdle)
}

// TestConnectMissingProfile connects to a profile that doesn't exist.
func TestConnectMissingProfile(t *testing.T) {
	h := newHarness(t)
	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: "ghost"})
	r := h.await()
	if r.Ok {
		t.Error("connect to missing profile should fail")
	}
	if h.runner.starts() != 0 {
		t.Error("runner should not start for a missing profile")
	}
}

// TestConnectExplicitNodeNotFound asks for a node id not in the profile.
func TestConnectExplicitNodeNotFound(t *testing.T) {
	h := newHarness(t)
	p := h.importFakeProfile()
	h.send(Request{ID: 2, Cmd: CmdConnect, Profile: p.ID, Node: "no-such-node"})
	r := h.await()
	if r.Ok {
		t.Error("connect with unknown node should fail")
	}
}

// perHostDialer returns an injectable dialer whose dial latency depends on the
// destination host, using the real clock so pingOne measures it. The returned
// RTTs are coarse (tens of ms) with wide gaps so the resulting latency order is
// deterministic despite scheduler jitter. An unknown host fails the dial,
// modelling an unreachable candidate.
func perHostDialer(byHost map[string]time.Duration) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, _ /*network*/, address string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			host = address
		}
		d, ok := byHost[host]
		if !ok {
			return nil, errors.New("unreachable host")
		}
		select {
		case <-time.After(d):
			return fakeConn{}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// TestConnectAutoPicksFastestNode verifies the auto strategy: with an injected
// per-host dialer giving each candidate a distinct RTT, connect (auto, no
// explicit node) must establish on the fastest one — the first candidate the
// latency-ordered fallback walk hands out, which the fake runner brings up.
func TestConnectAutoPicksFastestNode(t *testing.T) {
	h := newHarness(t)
	// Three nodes; the second address is the fastest to dial.
	p := h.addProfile([]model.Node{
		vlessNode("slow", "203.0.113.10"),
		vlessNode("fast", "203.0.113.20"),
		vlessNode("mid", "203.0.113.30"),
	})
	h.daemon.dial = perHostDialer(map[string]time.Duration{
		"203.0.113.10": 60 * time.Millisecond,
		"203.0.113.20": 3 * time.Millisecond,
		"203.0.113.30": 25 * time.Millisecond,
	})

	// The fake runner's probe succeeds on the first candidate, so the connected
	// node must be the fastest one ("fast").
	fastID := p.Servers[1].ID
	h.send(Request{ID: 2, Cmd: CmdConnect, Profile: p.ID, Auto: true})
	r := h.await()
	var st State
	h.dataInto(r, &st)
	if st.State != StateConnecting {
		t.Fatalf("connect response state = %q, want connecting", st.State)
	}

	connected := h.awaitState(StateConnected)
	if connected["node"] != fastID {
		t.Errorf("auto connected node = %v, want fastest %s", connected["node"], fastID)
	}
	h.send(Request{ID: 3, Cmd: CmdDisconnect})
	h.await()
}

// TestConnectAutoFallsBackPastBlockedFastest proves auto keeps the anti-DPI
// fallback: when the fastest candidate is blocked on its connect-probe, the loop
// advances to the next fastest rather than failing. failStarts=1 blocks only the
// first candidate handed out (the fastest), so the connection lands on the
// second-fastest.
func TestConnectAutoFallsBackPastBlockedFastest(t *testing.T) {
	h := newHarness(t)
	p := h.addProfile([]model.Node{
		vlessNode("fast", "203.0.113.20"),
		vlessNode("mid", "203.0.113.30"),
		vlessNode("slow", "203.0.113.10"),
	})
	h.daemon.dial = perHostDialer(map[string]time.Duration{
		"203.0.113.20": 3 * time.Millisecond,  // fastest, but will be blocked
		"203.0.113.30": 25 * time.Millisecond, // second fastest, comes up
		"203.0.113.10": 60 * time.Millisecond,
	})
	// Block the first candidate the walk hands out (the fastest); the next one
	// connects.
	h.runner.setFailStarts(1)

	midID := p.Servers[1].ID
	h.send(Request{ID: 2, Cmd: CmdConnect, Profile: p.ID, Auto: true})
	h.await()

	connected := h.awaitState(StateConnected)
	if connected["node"] != midID {
		t.Errorf("auto fallback connected node = %v, want second-fastest %s", connected["node"], midID)
	}
	h.send(Request{ID: 3, Cmd: CmdDisconnect})
	h.await()
}

// TestConnectAutoIgnoredWithExplicitNode confirms an explicit node wins over
// auto: the walk collapses to exactly that node even with Auto set, and no
// candidate pinging reorders anything. We point the explicit node at the
// slowest-to-dial server; if auto were (wrongly) in effect it would prefer a
// faster one.
func TestConnectAutoIgnoredWithExplicitNode(t *testing.T) {
	h := newHarness(t)
	p := h.addProfile([]model.Node{
		vlessNode("fast", "203.0.113.20"),
		vlessNode("slow", "203.0.113.10"),
	})
	h.daemon.dial = perHostDialer(map[string]time.Duration{
		"203.0.113.20": 3 * time.Millisecond,
		"203.0.113.10": 60 * time.Millisecond,
	})

	slowID := p.Servers[1].ID
	h.send(Request{ID: 2, Cmd: CmdConnect, Profile: p.ID, Node: slowID, Auto: true})
	h.await()

	connected := h.awaitState(StateConnected)
	if connected["node"] != slowID {
		t.Errorf("explicit+auto connected node = %v, want the explicit %s", connected["node"], slowID)
	}
	// Exactly one candidate was ever started: the collapse held.
	h.waitStarts(1)
	if got := h.runner.starts(); got != 1 {
		t.Errorf("explicit node started %d candidates, want 1", got)
	}
	h.send(Request{ID: 3, Cmd: CmdDisconnect})
	h.await()
}

// TestConnectAutoAllUnreachableStillConnects checks that when no candidate
// answers the cheap TCP ping, auto does not give up: every node is unreachable
// in the latency sense and sorts to the back in input order, but the walk still
// tries them and the fake runner brings the first up. (A server can refuse the
// probe yet complete a real handshake.)
func TestConnectAutoAllUnreachableStillConnects(t *testing.T) {
	h := newHarness(t)
	p := h.addProfile([]model.Node{
		vlessNode("a", "203.0.113.20"),
		vlessNode("b", "203.0.113.30"),
	})
	// Empty map: every dial fails, so no candidate has a usable RTT.
	h.daemon.dial = perHostDialer(map[string]time.Duration{})

	firstID := p.Servers[0].ID
	h.send(Request{ID: 2, Cmd: CmdConnect, Profile: p.ID, Auto: true})
	h.await()

	connected := h.awaitState(StateConnected)
	if connected["node"] != firstID {
		t.Errorf("all-unreachable auto connected node = %v, want input-order first %s", connected["node"], firstID)
	}
	h.send(Request{ID: 3, Cmd: CmdDisconnect})
	h.await()
}

// TestProcessExitBecomesError simulates sing-box dying during connect; the daemon
// must publish an error state, not hang. The single-node profile means a dead
// process exhausts the fallback walk straight to error.
func TestProcessExitBecomesError(t *testing.T) {
	h := newHarness(t)
	p := h.importFakeProfile()

	h.send(Request{ID: 2, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	h.awaitState(StateConnecting)

	// Wait until the loop has actually spawned the process before killing it —
	// before Start the runner's done channel is the never-fired placeholder, and
	// the loop runs Start in the background after returning connecting.
	h.waitStarts(1)
	h.runner.exit(errors.New("exit status 1"))

	ev := h.awaitState(StateError)
	if ev["error"] == nil || ev["error"] == "" {
		t.Errorf("error state event missing error message: %+v", ev)
	}
}

// TestTrafficEvents verifies traffic events carry totals and a computed rate
// across two polls.
func TestTrafficEvents(t *testing.T) {
	h := newHarness(t)
	p := h.importFakeProfile()

	h.runner.setStats(0, 0)
	h.send(Request{ID: 2, Cmd: CmdConnect, Profile: p.ID})
	h.await()

	// First poll establishes a baseline (rate 0), second yields a rate.
	first := h.awaitEvent(EventTraffic)
	_ = first
	h.runner.setStats(4096, 8192)
	// Wait for a traffic event that reflects the bump.
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-h.events:
			if ev["event"] != EventTraffic {
				continue
			}
			down, _ := ev["down"].(float64)
			if int64(down) == 8192 {
				up, _ := ev["up"].(float64)
				if int64(up) != 4096 {
					t.Errorf("traffic up = %v, want 4096", up)
				}
				// Rates should be non-negative; exact value depends on timing.
				if dr, _ := ev["down_rate"].(float64); dr < 0 {
					t.Errorf("down_rate negative: %v", dr)
				}
				return
			}
		case <-deadline:
			t.Fatal("never saw traffic event reflecting updated stats")
		}
	}
}

// TestPing exercises the ping command with an injected dialer so it stays
// offline and deterministic: a successful dial yields ok=true with a measured
// RTT, computed from the injected clock.
func TestPing(t *testing.T) {
	h := newHarness(t)
	p := h.importFakeProfile()

	// Inject a fake clock advancing 12ms per read and a dialer that succeeds, so
	// the reported RTT is exactly the elapsed fake time.
	var ticks int64
	h.daemon.now = func() time.Time {
		ticks++
		return time.Unix(0, ticks*12*int64(time.Millisecond))
	}
	h.daemon.dial = func(ctx context.Context, network, address string) (net.Conn, error) {
		return fakeConn{}, nil
	}

	h.send(Request{ID: 2, Cmd: CmdPing, Profile: p.ID})
	r := h.await()
	var out struct {
		Results []PingResult `json:"results"`
	}
	h.dataInto(r, &out)
	if len(out.Results) != 1 {
		t.Fatalf("ping returned %d results, want 1", len(out.Results))
	}
	got := out.Results[0]
	if got.Node != p.Servers[0].ID {
		t.Errorf("ping node = %q, want %q", got.Node, p.Servers[0].ID)
	}
	if !got.Ok {
		t.Errorf("ping ok = false, want true (dial was faked to succeed)")
	}
	if got.RTTMs != 12 {
		t.Errorf("ping rtt = %dms, want 12", got.RTTMs)
	}
}

// TestPingUnreachable confirms a failed dial is reported best-effort as ok=false
// rather than failing the whole command.
func TestPingUnreachable(t *testing.T) {
	h := newHarness(t)
	p := h.importFakeProfile()
	h.daemon.dial = func(ctx context.Context, network, address string) (net.Conn, error) {
		return nil, errors.New("connection refused")
	}
	h.send(Request{ID: 2, Cmd: CmdPing, Profile: p.ID})
	r := h.await()
	var out struct {
		Results []PingResult `json:"results"`
	}
	h.dataInto(r, &out)
	if len(out.Results) != 1 || out.Results[0].Ok {
		t.Errorf("unreachable ping = %+v, want one ok=false result", out.Results)
	}
}

// TestServeReturnsOnEOF confirms closing the input ends Serve cleanly.
func TestServeReturnsOnEOF(t *testing.T) {
	store, err := profile.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	d := NewDaemon(store, newFakeRunner())
	inR, inW := io.Pipe()
	var out discardWriter
	srv := NewServer(d, inR, &out)
	done := make(chan error, 1)
	go func() { done <- srv.Serve(context.Background()) }()

	inW.Close() // EOF
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Serve returned %v on clean EOF, want nil", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not return on EOF")
	}
}

// discardWriter is an io.Writer that ignores everything, safe for concurrent use
// (the daemon may emit while we write responses).
type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
