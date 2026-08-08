//go:build darwin

package macos

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
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

// TestStatsSendsClashSecret is the runner half of the clash API auth fix: when a
// secret is set, Stats must present it as a bearer token so the authenticated
// external controller answers.
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

// TestConnectionsJSONSendsClashSecret covers the live-host list's fetch: it must
// present the bearer token like the other clash calls and hand the document back
// untouched, since the control package is what parses it.
func TestConnectionsJSONSendsClashSecret(t *testing.T) {
	const body = `{"connections":[{"metadata":{"host":"example.com"},"upload":1,"download":2,"chains":["proxy"]}]}`
	var gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		gotAuth = req.Header.Get("Authorization")
		gotPath = req.URL.Path
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	r := New()
	r.ClashPort = clashPortFromURL(t, srv.URL)
	r.clashSecret = "s3cr3t"

	got, err := r.ConnectionsJSON(context.Background())
	if err != nil {
		t.Fatalf("ConnectionsJSON: %v", err)
	}
	if string(got) != body {
		t.Errorf("body = %s, want it returned verbatim: %s", got, body)
	}
	if gotAuth != "Bearer s3cr3t" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer s3cr3t")
	}
	if gotPath != "/connections" {
		t.Errorf("path = %q, want /connections", gotPath)
	}
}

// TestConnectionsJSONRejectsNon200 makes an unauthenticated or otherwise refused
// API an error rather than a body the caller would try to parse — the daemon
// turns it into "no list, here is why".
func TestConnectionsJSONRejectsNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Unauthorized"}`))
	}))
	defer srv.Close()

	r := New()
	r.ClashPort = clashPortFromURL(t, srv.URL)

	if _, err := r.ConnectionsJSON(context.Background()); err == nil {
		t.Fatal("a non-200 from the clash API must be an error")
	}
}

// TestConnectionsJSONHonoursContext proves a cancelled request does not outlive
// the caller: a UI that gave up must not leave the fetch outstanding.
func TestConnectionsJSONHonoursContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"connections":[]}`))
	}))
	defer srv.Close()

	r := New()
	r.ClashPort = clashPortFromURL(t, srv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.ConnectionsJSON(ctx); err == nil {
		t.Fatal("a cancelled context must abort the fetch")
	}
}
