package control

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/tenebra-vpn/tenebra/core/model"
	"github.com/tenebra-vpn/tenebra/core/profile"
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
			h.events <- cp
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
	if st.Profile != p.ID || st.Node != p.Servers[0].ID {
		t.Errorf("connect response = %+v, want profile %s node %s", st, p.ID, p.Servers[0].ID)
	}

	// The runner must have been started with a non-empty config.
	if h.runner.starts() != 1 {
		t.Errorf("runner started %d times, want 1", h.runner.starts())
	}
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

	// A connecting state event, then (the process stays up) a connected one.
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

// TestProcessExitBecomesError simulates sing-box dying after connect; the daemon
// must publish an error state, not hang.
func TestProcessExitBecomesError(t *testing.T) {
	h := newHarness(t)
	p := h.importFakeProfile()

	h.send(Request{ID: 2, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	h.awaitState(StateConnecting)

	// Kill the "process" before it would promote to connected.
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
