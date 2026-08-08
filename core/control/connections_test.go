package control

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Divaaaan/tenebra/core/profile"
)

// clashBody renders a clash API /connections payload from raw connection
// objects, so a test states only the fields it cares about.
func clashBody(conns ...string) []byte {
	return []byte(fmt.Sprintf(
		`{"downloadTotal":0,"uploadTotal":0,"connections":[%s],"memory":0}`,
		strings.Join(conns, ","),
	))
}

// clashConn renders one connection object with the fields the live-host list
// reads.
func clashConn(host, destIP, process, outbound string, up, down int64) string {
	return fmt.Sprintf(
		`{"metadata":{"host":%q,"destinationIP":%q,"process":%q},"upload":%d,"download":%d,"chains":[%q],"rule":"Match"}`,
		host, destIP, process, up, down, outbound,
	)
}

// connectDaemon forces the daemon into the connected state without going near
// sing-box: list_connections is gated on that state, and these tests judge the
// list, not the connect lifecycle.
func connectDaemon(d *Daemon) {
	d.mu.Lock()
	d.state.State = StateConnected
	d.mu.Unlock()
}

// findHost returns the row for host, or a zero row and false.
func findHost(rows []LiveConnection, host string) (LiveConnection, bool) {
	for _, r := range rows {
		if r.Host == host {
			return r, true
		}
	}
	return LiveConnection{}, false
}

// TestAggregateConnectionsMergesHostsAndSorts is the core of the feature: many
// live connections collapse into one row per host, with the byte counts summed,
// every source process named, and the busiest host first.
func TestAggregateConnectionsMergesHostsAndSorts(t *testing.T) {
	body := clashBody(
		clashConn("example.com", "93.184.216.34", "chrome.exe", "proxy", 100, 200),
		clashConn("example.com", "93.184.216.34", "chrome.exe", "proxy", 50, 150),
		clashConn("example.com", "93.184.216.34", "curl.exe", "proxy", 1, 2),
		clashConn("quiet.example", "198.51.100.9", "chrome.exe", "direct", 1, 1),
		clashConn("busy.example", "203.0.113.5", "player.exe", "direct", 10, 5000),
	)

	rows, err := aggregateConnections(body)
	if err != nil {
		t.Fatalf("aggregateConnections: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3 (one per host): %+v", len(rows), rows)
	}

	// Sorted by total traffic, heaviest first.
	wantOrder := []string{"busy.example", "example.com", "quiet.example"}
	for i, want := range wantOrder {
		if rows[i].Host != want {
			t.Errorf("row %d host = %q, want %q (rows must sort by traffic, heaviest first): %+v", i, rows[i].Host, want, rows)
		}
	}

	ex, ok := findHost(rows, "example.com")
	if !ok {
		t.Fatalf("example.com missing from %+v", rows)
	}
	if ex.Up != 151 || ex.Down != 352 {
		t.Errorf("example.com up,down = %d,%d, want 151,352 (bytes must be summed per host)", ex.Up, ex.Down)
	}
	// Both processes are named, busiest first, and neither is dropped.
	if ex.Process != "chrome.exe, curl.exe" {
		t.Errorf("example.com process = %q, want %q", ex.Process, "chrome.exe, curl.exe")
	}
	if ex.Outbound != "proxy" {
		t.Errorf("example.com outbound = %q, want %q", ex.Outbound, "proxy")
	}
	if ex.IsIP {
		t.Error("example.com must not be flagged as an address")
	}
}

// TestAggregateConnectionsMergesHostCase folds case-different spellings of the
// same domain into one row: DNS is case-insensitive, so two rows for
// Example.com and example.com would be one host shown twice.
func TestAggregateConnectionsMergesHostCase(t *testing.T) {
	body := clashBody(
		clashConn("Example.COM", "93.184.216.34", "chrome.exe", "proxy", 10, 20),
		clashConn("example.com", "93.184.216.34", "chrome.exe", "proxy", 5, 5),
	)

	rows, err := aggregateConnections(body)
	if err != nil {
		t.Fatalf("aggregateConnections: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1: %+v", len(rows), rows)
	}
	if rows[0].Host != "example.com" {
		t.Errorf("host = %q, want lowercased %q", rows[0].Host, "example.com")
	}
	if rows[0].Up != 15 || rows[0].Down != 25 {
		t.Errorf("up,down = %d,%d, want 15,25", rows[0].Up, rows[0].Down)
	}
}

// TestAggregateConnectionsFlagsAddressOnly covers the connection sniffing never
// named: the row falls back to the destination address and says so, because a
// rule written from an address matches only that address.
func TestAggregateConnectionsFlagsAddressOnly(t *testing.T) {
	body := clashBody(
		clashConn("", "203.0.113.9", "game.exe", "direct", 7, 9),
		// A literal address that arrived in the host field is still an address.
		clashConn("2001:db8::1", "2001:db8::1", "game.exe", "direct", 1, 1),
		clashConn("named.example", "203.0.113.10", "game.exe", "direct", 100, 100),
	)

	rows, err := aggregateConnections(body)
	if err != nil {
		t.Fatalf("aggregateConnections: %v", err)
	}

	ip, ok := findHost(rows, "203.0.113.9")
	if !ok {
		t.Fatalf("address-only connection missing from %+v", rows)
	}
	if !ip.IsIP {
		t.Error("address-only row must carry the address flag")
	}
	if ip.Up != 7 || ip.Down != 9 {
		t.Errorf("address row up,down = %d,%d, want 7,9", ip.Up, ip.Down)
	}

	v6, ok := findHost(rows, "2001:db8::1")
	if !ok {
		t.Fatalf("address in the host field missing from %+v", rows)
	}
	if !v6.IsIP {
		t.Error("an address carried in the host field must still be flagged")
	}

	named, ok := findHost(rows, "named.example")
	if !ok {
		t.Fatalf("named host missing from %+v", rows)
	}
	if named.IsIP {
		t.Error("a named host must not be flagged as an address")
	}
}

// TestAggregateConnectionsUsesProcessPath covers sing-box reporting only the
// full executable path: the row shows the file name, and a Windows path is cut
// correctly whatever OS this core was built for.
func TestAggregateConnectionsUsesProcessPath(t *testing.T) {
	body := []byte(`{"connections":[
		{"metadata":{"host":"a.example","processPath":"C:\\Program Files\\Browser\\browser.exe"},"upload":1,"download":1,"chains":["proxy"]},
		{"metadata":{"host":"b.example","processPath":"/usr/lib/browser/browser"},"upload":1,"download":1,"chains":["proxy"]}
	]}`)

	rows, err := aggregateConnections(body)
	if err != nil {
		t.Fatalf("aggregateConnections: %v", err)
	}
	a, ok := findHost(rows, "a.example")
	if !ok {
		t.Fatalf("a.example missing from %+v", rows)
	}
	if a.Process != "browser.exe" {
		t.Errorf("process = %q, want %q", a.Process, "browser.exe")
	}
	b, ok := findHost(rows, "b.example")
	if !ok {
		t.Fatalf("b.example missing from %+v", rows)
	}
	if b.Process != "browser" {
		t.Errorf("process = %q, want %q", b.Process, "browser")
	}
}

// TestAggregateConnectionsCapsList holds the list to something a person can
// read: the busiest maxLiveConnections hosts survive and the tail is dropped,
// rather than the response becoming a dump of every socket on the machine.
func TestAggregateConnectionsCapsList(t *testing.T) {
	conns := make([]string, 0, maxLiveConnections+50)
	for i := 0; i < maxLiveConnections+50; i++ {
		// Later hosts are busier, so the ones that must survive are the tail.
		conns = append(conns, clashConn(fmt.Sprintf("h%03d.example", i), "203.0.113.1", "p.exe", "proxy", int64(i), int64(i)))
	}

	rows, err := aggregateConnections(clashBody(conns...))
	if err != nil {
		t.Fatalf("aggregateConnections: %v", err)
	}
	if len(rows) != maxLiveConnections {
		t.Fatalf("got %d rows, want the cap of %d", len(rows), maxLiveConnections)
	}
	if want := fmt.Sprintf("h%03d.example", maxLiveConnections+49); rows[0].Host != want {
		t.Errorf("first row = %q, want the busiest host %q", rows[0].Host, want)
	}
	// The quietest hosts are the ones dropped.
	if _, ok := findHost(rows, "h000.example"); ok {
		t.Error("the quietest host survived the cap; the cap must drop the tail, not the head")
	}
}

// TestAggregateConnectionsRejectsMalformed proves a body that is not the payload
// we expect comes back as an error rather than a panic — the clash API is
// another process and its output is not ours to trust.
func TestAggregateConnectionsRejectsMalformed(t *testing.T) {
	for _, body := range []string{``, `{"connections":`, `not json at all`, `{"connections":{"a":1}}`} {
		if _, err := aggregateConnections([]byte(body)); err == nil {
			t.Errorf("aggregateConnections(%q) = nil error, want a parse error", body)
		}
	}
}

// TestAggregateConnectionsEmptyIsNotNil guards the wire shape: no connections
// must marshal as [] so the UI renders an empty list, not a missing one.
func TestAggregateConnectionsEmptyIsNotNil(t *testing.T) {
	rows, err := aggregateConnections([]byte(`{"connections":[]}`))
	if err != nil {
		t.Fatalf("aggregateConnections: %v", err)
	}
	if rows == nil {
		t.Fatal("empty result must be an empty slice, not nil")
	}
	raw, err := json.Marshal(connectionsResult{Connections: rows})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := string(raw); got != `{"connections":[]}` {
		t.Errorf("wire shape = %s, want {\"connections\":[]}", got)
	}
}

// TestListConnectionsRequiresConnection locks the gate. With no tunnel up there
// is no clash API to ask, so the command answers with a plain reason instead of
// a transport error — and never reaches the runner.
func TestListConnectionsRequiresConnection(t *testing.T) {
	d := newBareDaemon(t)
	r := d.runner.(*fakeRunner)
	r.setConnections(clashBody(clashConn("example.com", "", "chrome.exe", "proxy", 1, 1)), nil)

	resp := d.handleListConnections(context.Background(), Request{ID: 1, Cmd: CmdListConnections})
	if resp.Ok {
		t.Fatal("list_connections on an idle daemon must be refused")
	}
	if resp.Error == "" {
		t.Error("refusal must carry a reason the UI can show")
	}
	if n := r.connectionsCalls(); n != 0 {
		t.Errorf("runner was asked %d times despite no tunnel; the gate must come first", n)
	}
}

// TestListConnectionsReportsAPIFailure covers a live tunnel whose clash API does
// not answer (sing-box still starting, or gone): the command fails with the
// reason attached and carries no half-built list.
func TestListConnectionsReportsAPIFailure(t *testing.T) {
	d := newBareDaemon(t)
	connectDaemon(d)
	r := d.runner.(*fakeRunner)
	r.setConnections(nil, fmt.Errorf("dial tcp 127.0.0.1:9090: connection refused"))

	resp := d.handleListConnections(context.Background(), Request{ID: 2, Cmd: CmdListConnections})
	if resp.Ok {
		t.Fatal("an unreachable clash API must surface as a failed response")
	}
	if !strings.Contains(resp.Error, "connection refused") {
		t.Errorf("error = %q, want the underlying cause included", resp.Error)
	}
	if len(resp.Data) != 0 {
		t.Errorf("failed response must carry no data, got %s", resp.Data)
	}
}

// TestListConnectionsDispatched proves list_connections is wired into Handle and
// answered in the shape the UI decodes. A missing dispatch case would come back
// as an "unknown command" error instead.
func TestListConnectionsDispatched(t *testing.T) {
	h := newHarness(t)
	connectDaemon(h.daemon)
	h.daemon.runner.(*fakeRunner).setConnections(clashBody(
		clashConn("example.com", "93.184.216.34", "chrome.exe", "proxy", 1234, 56789),
	), nil)

	h.send(Request{ID: 20, Cmd: CmdListConnections})
	r := h.await()
	if !r.Ok {
		t.Fatalf("list_connections failed (is it dispatched in Handle?): %s", r.Error)
	}
	if r.ID != 20 {
		t.Errorf("response id = %d, want 20", r.ID)
	}

	var res connectionsResult
	h.dataInto(r, &res)
	if len(res.Connections) != 1 {
		t.Fatalf("got %d rows, want 1: %+v", len(res.Connections), res.Connections)
	}
	got := res.Connections[0]
	if got.Host != "example.com" || got.Process != "chrome.exe" || got.Up != 1234 || got.Down != 56789 || got.Outbound != "proxy" {
		t.Errorf("row = %+v, want example.com/chrome.exe/1234/56789/proxy", got)
	}

	// The Rust/TS sides deserialize by these exact wire keys.
	var raw struct {
		Connections []map[string]json.RawMessage `json:"connections"`
	}
	if err := json.Unmarshal(r.Data, &raw); err != nil {
		t.Fatalf("decode raw data: %v", err)
	}
	if len(raw.Connections) != 1 {
		t.Fatalf("raw wire shape: %s", r.Data)
	}
	for _, key := range []string{"host", "process", "up", "down", "outbound"} {
		if _, ok := raw.Connections[0][key]; !ok {
			t.Errorf("row missing %q key; wire shape: %s", key, r.Data)
		}
	}
}

// TestListConnectionsEmptyWhenNothingFlows covers a live tunnel that simply has
// no traffic: an empty list, not an error.
func TestListConnectionsEmptyWhenNothingFlows(t *testing.T) {
	d := newBareDaemon(t)
	connectDaemon(d)
	d.runner.(*fakeRunner).setConnections([]byte(`{"connections":[]}`), nil)

	resp := d.handleListConnections(context.Background(), Request{ID: 3, Cmd: CmdListConnections})
	if !resp.Ok {
		t.Fatalf("a quiet tunnel must answer ok, got %q", resp.Error)
	}
	if got := string(resp.Data); got != `{"connections":[]}` {
		t.Errorf("data = %s, want {\"connections\":[]}", got)
	}
}

// clashRunner is a fakeRunner whose ConnectionsJSON really speaks HTTP to a
// stand-in clash API the way an adapter's runner does: it holds the secret,
// presents it as a bearer, and hands back the raw body. A fake returning canned
// bytes could not show that the token stays inside the runner, which is the one
// thing the privacy test has to prove.
type clashRunner struct {
	*fakeRunner
	baseURL string
	secret  string
}

func (r *clashRunner) ConnectionsJSON(ctx context.Context) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.baseURL+"/connections", nil)
	if err != nil {
		return nil, err
	}
	if r.secret != "" {
		req.Header.Set("Authorization", "Bearer "+r.secret)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("clash connections: status %s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

// TestListConnectionsKeepsClashSecretOffTheWire is the privacy half: the token
// that unlocks the clash API authenticates the fetch and must appear nowhere in
// what the UI receives. Anyone holding it could read every connection on the
// machine and repoint outbounds.
func TestListConnectionsKeepsClashSecretOffTheWire(t *testing.T) {
	const secret = "s3cr3t-clash-token"
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		gotAuth = req.Header.Get("Authorization")
		_, _ = w.Write(clashBody(clashConn("example.com", "93.184.216.34", "chrome.exe", "proxy", 1, 2)))
	}))
	defer srv.Close()

	store, err := profile.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	d := NewDaemon(store, &clashRunner{fakeRunner: newFakeRunner(), baseURL: srv.URL, secret: secret})
	connectDaemon(d)

	resp := d.handleListConnections(context.Background(), Request{ID: 4, Cmd: CmdListConnections})
	if !resp.Ok {
		t.Fatalf("list_connections: %s", resp.Error)
	}
	if gotAuth != "Bearer "+secret {
		t.Errorf("Authorization = %q, want the bearer secret", gotAuth)
	}

	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	if strings.Contains(string(raw), secret) {
		t.Errorf("response leaked the clash API secret: %s", raw)
	}
	if strings.Contains(strings.ToLower(string(raw)), "authorization") {
		t.Errorf("response leaked the authorization header: %s", raw)
	}
}

// TestListConnectionsNotLogged holds the privacy rule that outranks the feature:
// the hosts a person visits are the most sensitive data this app touches, so the
// command must not emit them as log events (which the UI shows and diagnostics
// collect).
func TestListConnectionsNotLogged(t *testing.T) {
	const host = "private-host.example"
	h := newHarness(t)
	connectDaemon(h.daemon)
	h.daemon.runner.(*fakeRunner).setConnections(clashBody(
		clashConn(host, "203.0.113.77", "browser.exe", "proxy", 10, 20),
	), nil)

	h.send(Request{ID: 21, Cmd: CmdListConnections})
	if r := h.await(); !r.Ok {
		t.Fatalf("list_connections: %s", r.Error)
	}

	// Drain whatever the daemon published while answering; none of it may name
	// the host or the process behind it.
	deadline := time.After(200 * time.Millisecond)
	for {
		select {
		case ev := <-h.events:
			raw, err := json.Marshal(ev)
			if err != nil {
				t.Fatalf("marshal event: %v", err)
			}
			if strings.Contains(string(raw), host) || strings.Contains(string(raw), "browser.exe") {
				t.Fatalf("live host leaked into an event: %s", raw)
			}
		case <-deadline:
			return
		}
	}
}
