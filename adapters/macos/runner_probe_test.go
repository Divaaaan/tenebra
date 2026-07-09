//go:build darwin

package macos

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func TestDelayURL(t *testing.T) {
	got := delayURL(9090, "proxy")

	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("delayURL produced unparseable URL %q: %v", got, err)
	}
	if u.Scheme != "http" || u.Host != "127.0.0.1:9090" {
		t.Errorf("scheme/host = %q/%q, want http/127.0.0.1:9090", u.Scheme, u.Host)
	}
	if u.Path != "/proxies/proxy/delay" {
		t.Errorf("path = %q, want /proxies/proxy/delay", u.Path)
	}
	q := u.Query()
	if q.Get("timeout") != strconv.Itoa(probeTimeoutMs) {
		t.Errorf("timeout = %q, want %d", q.Get("timeout"), probeTimeoutMs)
	}
	if q.Get("url") != probeURL {
		t.Errorf("url param = %q, want %q", q.Get("url"), probeURL)
	}
}

func TestDelayURLPort(t *testing.T) {
	got := delayURL(7777, "proxy")
	if !strings.Contains(got, "127.0.0.1:7777") {
		t.Errorf("delayURL(7777, ...) = %q, want it to target port 7777", got)
	}
}

// TestDelayURLEscapesTag guards against a selector name with reserved characters
// breaking out of the path segment or smuggling extra query params.
func TestDelayURLEscapesTag(t *testing.T) {
	got := delayURL(9090, "weird name/with?reserved&chars")

	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("delayURL with reserved tag produced unparseable URL %q: %v", got, err)
	}
	// The escaped tag must round-trip back to the original through the path.
	wantPath := "/proxies/weird name/with?reserved&chars/delay"
	if u.Path != wantPath {
		t.Errorf("decoded path = %q, want %q", u.Path, wantPath)
	}
	// The tag's '&' must not have injected an extra query key.
	if u.Query().Get("reserved") != "" {
		t.Error("reserved character in tag leaked into the query string")
	}
}

func TestParseDelay(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		want    int
		wantErr bool
	}{
		{name: "typical", body: `{"delay":123}`, want: 123},
		{name: "zero", body: `{"delay":0}`, want: 0},
		{name: "extra fields ignored", body: `{"delay":42,"mean_delay":40}`, want: 42},
		{name: "missing field defaults zero", body: `{"mean_delay":40}`, want: 0},
		{name: "malformed", body: `{not json`, wantErr: true},
		{name: "empty", body: ``, wantErr: true},
		{name: "wrong type", body: `{"delay":"slow"}`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseDelay([]byte(tt.body))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseDelay(%q): want error, got nil", tt.body)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseDelay(%q): %v", tt.body, err)
			}
			if got != tt.want {
				t.Errorf("parseDelay(%q) = %d, want %d", tt.body, got, tt.want)
			}
		})
	}
}

func TestTrimBody(t *testing.T) {
	if got := trimBody([]byte("short")); got != "short" {
		t.Errorf("trimBody(short) = %q, want short", got)
	}
	long := strings.Repeat("x", 500)
	got := trimBody([]byte(long))
	if len(got) != 200 {
		t.Errorf("trimBody truncated to %d bytes, want 200", len(got))
	}
}

func TestProbeAgainstDeadPort(t *testing.T) {
	// Nothing listens on port 1; Probe must return a transport error, not panic.
	r := New()
	r.ClashPort = 1
	if _, err := r.Probe(context.Background(), "proxy"); err == nil {
		t.Error("Probe against a dead port should error")
	}
}

func TestProbeContextCancelled(t *testing.T) {
	// A cancelled context fails the request before it leaves; Probe surfaces it.
	r := New()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.Probe(ctx, "proxy"); err == nil {
		t.Error("Probe with a cancelled context should error")
	}
}
