package control

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Divaaaan/tenebra/core/model"
	"github.com/Divaaaan/tenebra/core/profile"
)

// fakeEcho builds a single ipEcho that returns body for url through the daemon's
// injected httpGet. The parse function is the real one under test so the test
// exercises body parsing, not a stand-in.
func fakeEcho(name, url string, parse func([]byte) (ip, country string)) ipEcho {
	return ipEcho{name: name, url: url, parse: parse}
}

// stubGetter returns an httpGet that answers from a fixed url->response table.
// A url present with a nil error returns its body; a url mapped to an error
// returns that error (modelling an unreachable endpoint); an unmapped url is a
// hard test failure so a typo can't silently look like a network failure.
func stubGetter(t *testing.T, responses map[string]stubResp) func(ctx context.Context, url string) ([]byte, error) {
	t.Helper()
	return func(_ context.Context, url string) ([]byte, error) {
		r, ok := responses[url]
		if !ok {
			t.Fatalf("unexpected httpGet url %q", url)
		}
		if r.err != nil {
			return nil, r.err
		}
		return []byte(r.body), nil
	}
}

type stubResp struct {
	body string
	err  error
}

// newBareDaemon builds a daemon with no server and no network, ready for a
// direct runLeakCheck call. The echo/DNS endpoints and getter are injected by
// the caller per case.
func newBareDaemon(t *testing.T) *Daemon {
	t.Helper()
	store, err := profile.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	return NewDaemon(store, newFakeRunner())
}

// connectTo forces the daemon into a connected state pointing at a one-node
// profile whose single server has the given address, so exitServer() resolves to
// it. It bypasses the real connect path (which needs sing-box) because these
// tests judge the IP-vs-exit verdict, not the lifecycle. The node protocol is a
// renderable one so the profile is well-formed.
func connectTo(t *testing.T, d *Daemon, serverAddr string) {
	t.Helper()
	node := model.Node{
		Name:     "exit",
		Protocol: model.VLESS,
		Server:   serverAddr,
		Port:     443,
	}
	p, err := profile.NewProfile("Test", profile.SourceManual, "", []model.Node{node})
	if err != nil {
		t.Fatalf("new profile: %v", err)
	}
	if err := d.store.Add(p); err != nil {
		t.Fatalf("add profile: %v", err)
	}
	d.mu.Lock()
	d.state = State{State: StateConnected, Profile: p.ID, Node: p.Servers[0].ID}
	d.mu.Unlock()
}

const (
	echoIPify    = "https://echo-1.test/ipify"
	echoIfconfig = "https://echo-2.test/ifconfig"
	echoText     = "https://echo-3.test/text"
	dnsEcho      = "https://dns.test/edns"
)

func TestRunLeakCheck(t *testing.T) {
	tests := []struct {
		name string
		// connect, when non-empty, puts the daemon in a connected state with this
		// exit address before the check.
		connect string
		// echoes overrides the daemon's echo list; nil keeps a single ipify echo.
		echoes []ipEcho
		// responses is the url->response table the injected getter answers from.
		responses map[string]stubResp

		wantIP       string
		wantCountry  string
		wantSource   string
		wantVerdict  Verdict
		wantExit     ExitVerdict
		wantDNS      DNSStatus
		wantResolver []string
	}{
		{
			name:        "idle ipify json",
			echoes:      []ipEcho{fakeEcho("ipify", echoIPify, parseIPJSON)},
			responses:   map[string]stubResp{echoIPify: {body: `{"ip":"203.0.113.7"}`}, dnsEcho: {err: errors.New("offline")}},
			wantIP:      "203.0.113.7",
			wantSource:  "ipify",
			wantVerdict: VerdictNeutral,
			wantExit:    "", // not connected -> no exit verdict
			wantDNS:     DNSUnavailable,
		},
		{
			name:        "idle ifconfig json carries country",
			echoes:      []ipEcho{fakeEcho("ifconfig.co", echoIfconfig, parseIfconfigJSON)},
			responses:   map[string]stubResp{echoIfconfig: {body: `{"ip":"203.0.113.9","country_iso":"DE"}`}, dnsEcho: {err: errors.New("offline")}},
			wantIP:      "203.0.113.9",
			wantCountry: "DE",
			wantSource:  "ifconfig.co",
			wantVerdict: VerdictNeutral,
			wantDNS:     DNSUnavailable,
		},
		{
			name:        "idle bare-text icanhazip",
			echoes:      []ipEcho{fakeEcho("icanhazip", echoText, parseIPText)},
			responses:   map[string]stubResp{echoText: {body: "198.51.100.5\n"}, dnsEcho: {err: errors.New("offline")}},
			wantIP:      "198.51.100.5",
			wantSource:  "icanhazip",
			wantVerdict: VerdictNeutral,
			wantDNS:     DNSUnavailable,
		},
		{
			name: "multi-endpoint fallback: first fails, second answers",
			echoes: []ipEcho{
				fakeEcho("ipify", echoIPify, parseIPJSON),
				fakeEcho("ifconfig.co", echoIfconfig, parseIfconfigJSON),
			},
			responses: map[string]stubResp{
				echoIPify:    {err: errors.New("connection refused")},
				echoIfconfig: {body: `{"ip":"203.0.113.42","country_iso":"NL"}`},
				dnsEcho:      {err: errors.New("offline")},
			},
			wantIP:      "203.0.113.42",
			wantCountry: "NL",
			wantSource:  "ifconfig.co",
			wantVerdict: VerdictNeutral,
			wantDNS:     DNSUnavailable,
		},
		{
			name: "fallback skips a body with no usable ip",
			echoes: []ipEcho{
				fakeEcho("ipify", echoIPify, parseIPJSON),
				fakeEcho("icanhazip", echoText, parseIPText),
			},
			responses: map[string]stubResp{
				echoIPify: {body: `{"ip":"not-an-ip"}`},
				echoText:  {body: "192.0.2.77"},
				dnsEcho:   {err: errors.New("offline")},
			},
			wantIP:      "192.0.2.77",
			wantSource:  "icanhazip",
			wantVerdict: VerdictNeutral,
			wantDNS:     DNSUnavailable,
		},
		{
			name:        "connected, literal exit matches -> ok",
			connect:     "203.0.113.7",
			echoes:      []ipEcho{fakeEcho("ipify", echoIPify, parseIPJSON)},
			responses:   map[string]stubResp{echoIPify: {body: `{"ip":"203.0.113.7"}`}, dnsEcho: {err: errors.New("offline")}},
			wantIP:      "203.0.113.7",
			wantSource:  "ipify",
			wantVerdict: VerdictOK,
			wantExit:    ExitMatchMatch,
			wantDNS:     DNSUnavailable,
		},
		{
			name:        "connected, literal exit mismatch -> warn (probable leak)",
			connect:     "203.0.113.7",
			echoes:      []ipEcho{fakeEcho("ipify", echoIPify, parseIPJSON)},
			responses:   map[string]stubResp{echoIPify: {body: `{"ip":"198.51.100.250"}`}, dnsEcho: {err: errors.New("offline")}},
			wantIP:      "198.51.100.250",
			wantSource:  "ipify",
			wantVerdict: VerdictWarn,
			wantExit:    ExitMatchMismatch,
			wantDNS:     DNSUnavailable,
		},
		{
			name:        "connected, hostname exit -> unknown (not resolved)",
			connect:     "exit.example.com",
			echoes:      []ipEcho{fakeEcho("ipify", echoIPify, parseIPJSON)},
			responses:   map[string]stubResp{echoIPify: {body: `{"ip":"203.0.113.7"}`}, dnsEcho: {err: errors.New("offline")}},
			wantIP:      "203.0.113.7",
			wantSource:  "ipify",
			wantVerdict: VerdictNeutral,
			wantExit:    ExitMatchUnknown,
			wantDNS:     DNSUnavailable,
		},
		{
			name:        "no ip from any echo -> error",
			echoes:      []ipEcho{fakeEcho("ipify", echoIPify, parseIPJSON)},
			responses:   map[string]stubResp{echoIPify: {err: errors.New("connection refused")}, dnsEcho: {err: errors.New("offline")}},
			wantIP:      "",
			wantSource:  "",
			wantVerdict: VerdictError,
			wantDNS:     DNSUnavailable,
		},
		{
			name:        "connected but no ip -> error with unknown exit",
			connect:     "203.0.113.7",
			echoes:      []ipEcho{fakeEcho("ipify", echoIPify, parseIPJSON)},
			responses:   map[string]stubResp{echoIPify: {err: errors.New("connection refused")}, dnsEcho: {err: errors.New("offline")}},
			wantIP:      "",
			wantVerdict: VerdictError,
			wantExit:    ExitMatchUnknown,
			wantDNS:     DNSUnavailable,
		},
		{
			name:         "dns probe reveals a resolver -> inconclusive (never a pass)",
			echoes:       []ipEcho{fakeEcho("ipify", echoIPify, parseIPJSON)},
			responses:    map[string]stubResp{echoIPify: {body: `{"ip":"203.0.113.7"}`}, dnsEcho: {body: `{"dns":{"ip":"1.1.1.1"}}`}},
			wantIP:       "203.0.113.7",
			wantSource:   "ipify",
			wantVerdict:  VerdictNeutral,
			wantDNS:      DNSInconclusive,
			wantResolver: []string{"1.1.1.1"},
		},
		{
			name:        "dns probe answers without a resolver -> inconclusive, no addrs",
			echoes:      []ipEcho{fakeEcho("ipify", echoIPify, parseIPJSON)},
			responses:   map[string]stubResp{echoIPify: {body: `{"ip":"203.0.113.7"}`}, dnsEcho: {body: `{"unrelated":"value"}`}},
			wantIP:      "203.0.113.7",
			wantSource:  "ipify",
			wantVerdict: VerdictNeutral,
			wantDNS:     DNSInconclusive,
		},
		{
			name:        "dns endpoint unreachable -> unavailable (never a pass)",
			echoes:      []ipEcho{fakeEcho("ipify", echoIPify, parseIPJSON)},
			responses:   map[string]stubResp{echoIPify: {body: `{"ip":"203.0.113.7"}`}, dnsEcho: {err: errors.New("timeout")}},
			wantIP:      "203.0.113.7",
			wantSource:  "ipify",
			wantVerdict: VerdictNeutral,
			wantDNS:     DNSUnavailable,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := newBareDaemon(t)
			if tc.connect != "" {
				connectTo(t, d, tc.connect)
			}
			d.httpGet = stubGetter(t, tc.responses)
			d.ipEchoes = tc.echoes
			d.dnsEchoURL = dnsEcho

			res := d.runLeakCheck(context.Background())

			if res.PublicIP != tc.wantIP {
				t.Errorf("PublicIP = %q, want %q", res.PublicIP, tc.wantIP)
			}
			if res.Country != tc.wantCountry {
				t.Errorf("Country = %q, want %q", res.Country, tc.wantCountry)
			}
			if res.Source != tc.wantSource {
				t.Errorf("Source = %q, want %q", res.Source, tc.wantSource)
			}
			if res.IPVerdict != tc.wantVerdict {
				t.Errorf("IPVerdict = %q, want %q", res.IPVerdict, tc.wantVerdict)
			}
			if res.ExitMatch != tc.wantExit {
				t.Errorf("ExitMatch = %q, want %q", res.ExitMatch, tc.wantExit)
			}
			if res.IPMessage == "" {
				t.Error("IPMessage is empty; the result must always carry a human summary")
			}
			if res.DNS.Status != tc.wantDNS {
				t.Errorf("DNS.Status = %q, want %q", res.DNS.Status, tc.wantDNS)
			}
			if res.DNS.Message == "" {
				t.Error("DNS.Message is empty; the DNS result must always explain itself")
			}
			// A non-ok/leak DNS status must never read as safe to the UI: assert the
			// two undeterminable states are exactly what they claim and carry no
			// fabricated pass.
			if tc.wantDNS == DNSUnavailable || tc.wantDNS == DNSInconclusive {
				if res.DNS.Status == DNSOK {
					t.Error("undeterminable DNS reported as ok — that would be a fabricated pass")
				}
			}
			if !equalStrings(res.DNS.Resolvers, tc.wantResolver) {
				t.Errorf("DNS.Resolvers = %v, want %v", res.DNS.Resolvers, tc.wantResolver)
			}

			// The connected flag must mirror whether we forced a connection.
			if res.Connected != (tc.connect != "") {
				t.Errorf("Connected = %v, want %v", res.Connected, tc.connect != "")
			}
			// ExitServer is only meaningful (and only present) when connected.
			if tc.connect != "" && res.ExitServer != tc.connect {
				t.Errorf("ExitServer = %q, want %q", res.ExitServer, tc.connect)
			}
			if tc.connect == "" && res.ExitServer != "" {
				t.Errorf("ExitServer = %q on an idle check, want empty", res.ExitServer)
			}
		})
	}
}

// TestMatchExit covers the literal-vs-hostname exit comparison in isolation, the
// pure heart of the verdict: literal IPs compare exactly, a hostname is honestly
// "unknown" rather than guessed, and a stray port is stripped defensively.
func TestMatchExit(t *testing.T) {
	tests := []struct {
		name     string
		publicIP string
		exit     string
		want     ExitVerdict
	}{
		{"literal match", "203.0.113.7", "203.0.113.7", ExitMatchMatch},
		{"literal mismatch", "203.0.113.7", "198.51.100.9", ExitMatchMismatch},
		{"hostname is unknown", "203.0.113.7", "exit.example.com", ExitMatchUnknown},
		{"empty exit is unknown", "203.0.113.7", "", ExitMatchUnknown},
		{"exit with stray port still matches host", "203.0.113.7", "203.0.113.7:443", ExitMatchMatch},
		{"unparsable public ip is unknown", "garbage", "203.0.113.7", ExitMatchUnknown},
		{"ipv6 literal match", "2001:db8::1", "2001:db8::1", ExitMatchMatch},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := matchExit(tc.publicIP, tc.exit); got != tc.want {
				t.Errorf("matchExit(%q, %q) = %q, want %q", tc.publicIP, tc.exit, got, tc.want)
			}
		})
	}
}

// TestParseDNSResolvers checks the resolver extraction accepts the field shapes
// the probe may return and keeps only valid, de-duplicated IPs.
func TestParseDNSResolvers(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []string
	}{
		{"dns.ip shape", `{"dns":{"ip":"1.1.1.1"}}`, []string{"1.1.1.1"}},
		{"edns.ip shape", `{"edns":{"ip":"9.9.9.9"}}`, []string{"9.9.9.9"}},
		{"resolver string", `{"resolver":"8.8.8.8"}`, []string{"8.8.8.8"}},
		{"resolvers array", `{"resolvers":["8.8.8.8","8.8.4.4"]}`, []string{"8.8.8.8", "8.8.4.4"}},
		{"de-duplicates across fields", `{"dns":{"ip":"1.1.1.1"},"resolver":"1.1.1.1"}`, []string{"1.1.1.1"}},
		{"drops non-ip values", `{"resolver":"not-an-ip","dns":{"ip":"1.0.0.1"}}`, []string{"1.0.0.1"}},
		{"no resolver fields -> nil", `{"something":"else"}`, nil},
		{"malformed json -> nil", `{nope`, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseDNSResolvers([]byte(tc.body))
			if !equalStrings(got, tc.want) {
				t.Errorf("parseDNSResolvers(%s) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}

// TestLeakCheckDispatched is the dispatch proof: a leak_check request issued over
// the real server loop must be routed to the handler and answered with a
// well-formed result. The endpoints are stubbed on the daemon so nothing dials
// out. This proves the command is wired into Handle (a missing case would come
// back as an "unknown command" error instead).
func TestLeakCheckDispatched(t *testing.T) {
	h := newHarness(t)
	// Point the leak check at fake endpoints with canned bodies so the handler
	// runs fully offline through the same daemon the server drives.
	h.daemon.httpGet = stubGetter(t, map[string]stubResp{
		echoIPify: {body: `{"ip":"203.0.113.7"}`},
		dnsEcho:   {body: `{"dns":{"ip":"1.1.1.1"}}`},
	})
	h.daemon.ipEchoes = []ipEcho{fakeEcho("ipify", echoIPify, parseIPJSON)}
	h.daemon.dnsEchoURL = dnsEcho

	h.send(Request{ID: 9, Cmd: CmdLeakCheck})
	r := h.await()
	if !r.Ok {
		t.Fatalf("leak_check failed (is it dispatched in Handle?): %s", r.Error)
	}
	if r.ID != 9 {
		t.Errorf("response id = %d, want 9", r.ID)
	}

	var res LeakCheck
	h.dataInto(r, &res)

	// Idle store -> not connected, neutral IP verdict, the IP we fed back.
	if res.Connected {
		t.Errorf("Connected = true on a fresh idle store, want false")
	}
	if res.PublicIP != "203.0.113.7" {
		t.Errorf("PublicIP = %q, want 203.0.113.7", res.PublicIP)
	}
	if res.Source != "ipify" {
		t.Errorf("Source = %q, want ipify", res.Source)
	}
	if res.IPVerdict != VerdictNeutral {
		t.Errorf("IPVerdict = %q, want neutral", res.IPVerdict)
	}
	if res.IPMessage == "" {
		t.Error("IPMessage empty in dispatched result")
	}
	// DNS revealed a resolver but stays inconclusive — never a fabricated pass.
	if res.DNS.Status != DNSInconclusive {
		t.Errorf("DNS.Status = %q, want inconclusive", res.DNS.Status)
	}
	if len(res.DNS.Resolvers) != 1 || res.DNS.Resolvers[0] != "1.1.1.1" {
		t.Errorf("DNS.Resolvers = %v, want [1.1.1.1]", res.DNS.Resolvers)
	}

	// And the raw wire form must carry the documented keys, since the Rust/TS
	// sides deserialize by these exact names.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(r.Data, &raw); err != nil {
		t.Fatalf("decode raw data: %v", err)
	}
	for _, key := range []string{"connected", "ip_verdict", "ip_message", "dns", "public_ip", "source"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("leak_check result missing %q key; wire shape: %s", key, r.Data)
		}
	}
}

// equalStrings reports slice equality treating nil and empty as equal, which is
// what the omitempty Resolvers field needs (a nil and a len-0 slice are the same
// "no resolvers" result).
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
