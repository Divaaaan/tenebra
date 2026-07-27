//go:build linux

package linux

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
