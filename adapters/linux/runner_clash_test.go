//go:build linux

package linux

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// clashPortFromURL parses the port out of an httptest server URL so a Runner can
// be pointed at it.
func clashPortFromURL(t *testing.T, raw string) int {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse test server url %q: %v", raw, err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("test server url %q has no numeric port: %v", raw, err)
	}
	return port
}

func TestClashSecretFromConfig(t *testing.T) {
	tests := []struct {
		name string
		cfg  string
		want string
	}{
		{
			name: "present",
			cfg:  `{"experimental":{"clash_api":{"external_controller":"127.0.0.1:9090","secret":"abc123"}}}`,
			want: "abc123",
		},
		{
			name: "absent",
			cfg:  `{"experimental":{"clash_api":{"external_controller":"127.0.0.1:9090"}}}`,
			want: "",
		},
		{name: "no clash block", cfg: `{"experimental":{}}`, want: ""},
		{name: "empty object", cfg: `{}`, want: ""},
		{name: "malformed", cfg: `{not json`, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := clashSecretFromConfig([]byte(tt.cfg)); got != tt.want {
				t.Errorf("clashSecretFromConfig(%s) = %q, want %q", tt.cfg, got, tt.want)
			}
		})
	}
}

// TestStatsSendsClashSecret: when a secret is set, Stats must present it as a
// bearer token so the authenticated external controller answers.
func TestStatsSendsClashSecret(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		gotAuth = req.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"downloadTotal":51200,"uploadTotal":10240}`))
	}))
	defer srv.Close()

	r := New()
	r.ClashPort = clashPortFromURL(t, srv.URL)
	r.clashSecret = "s3cr3t"

	up, down, err := r.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if up != 10240 || down != 51200 {
		t.Errorf("up,down = %d,%d, want 10240,51200", up, down)
	}
	if gotAuth != "Bearer s3cr3t" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer s3cr3t")
	}
}

// TestProbeSendsClashSecret mirrors TestStatsSendsClashSecret for the delay
// probe: it too must carry the bearer token.
func TestProbeSendsClashSecret(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		gotAuth = req.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"delay":42}`))
	}))
	defer srv.Close()

	r := New()
	r.ClashPort = clashPortFromURL(t, srv.URL)
	r.clashSecret = "tok"

	delay, err := r.Probe(context.Background(), "proxy")
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if delay != 42 {
		t.Errorf("delay = %d, want 42", delay)
	}
	if gotAuth != "Bearer tok" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer tok")
	}
}

// TestStatsOmitsAuthWhenNoSecret guards that a secretless config (the token is
// unguessable-random in production, but a test or older path may omit it) sends
// no Authorization header rather than an empty bearer.
func TestStatsOmitsAuthWhenNoSecret(t *testing.T) {
	var hadAuth bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_, hadAuth = req.Header["Authorization"]
		_, _ = w.Write([]byte(`{"downloadTotal":0,"uploadTotal":0}`))
	}))
	defer srv.Close()

	r := New()
	r.ClashPort = clashPortFromURL(t, srv.URL)
	// clashSecret deliberately left empty.

	if _, _, err := r.Stats(); err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if hadAuth {
		t.Error("no Authorization header should be sent when the config carries no secret")
	}
}

// largeConnectionsBody renders a /connections document in the shape sing-box
// serves on a busy machine: the two byte totals at the head, then a live-socket
// list long enough to carry the response past minBytes. The list is the bulk of
// a real response, and the counters are two fields sitting behind it.
func largeConnectionsBody(minBytes int) []byte {
	var b strings.Builder
	b.WriteString(`{"downloadTotal":51200,"uploadTotal":10240,"connections":[`)
	for i := 0; b.Len() < minBytes; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"id":"%032x","upload":%d,"download":%d,"start":"2026-08-09T00:00:00Z","chains":["proxy"],"rule":"route","metadata":{"network":"tcp","sourceIP":"10.0.0.%d","destinationIP":"93.184.216.%d","host":"host-%d.example.com","destinationPort":"443"}}`,
			i, i, i*2, i%256, i%256, i)
	}
	b.WriteString(`],"memory":0}`)
	return []byte(b.String())
}

// TestStatsReadsABodyPastAMegabyte pins the traffic counters against the read
// limit that used to kill them. The totals sit at the head of the /connections
// object, but the body is parsed as one JSON value, so json.Unmarshal has to walk
// the whole connection list to reach them: a document cut short is a parse error,
// not a shorter answer. The read stopped at a megabyte, which a few thousand live
// sockets pass easily — so on exactly the machines that move traffic, Stats began
// failing every poll and the graph in the interface froze with nothing logged and
// nothing shown. The body here is deliberately past that old limit.
func TestStatsReadsABodyPastAMegabyte(t *testing.T) {
	body := largeConnectionsBody(1 << 20)
	if len(body) <= 1<<20 {
		t.Fatalf("test body is %d bytes, want it past the megabyte the read used to stop at", len(body))
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	r := New()
	r.ClashPort = clashPortFromURL(t, srv.URL)

	up, down, err := r.Stats()
	if err != nil {
		t.Fatalf("Stats on a %d-byte body: %v", len(body), err)
	}
	if up != 10240 || down != 51200 {
		t.Errorf("up,down = %d,%d, want 10240,51200", up, down)
	}
}
