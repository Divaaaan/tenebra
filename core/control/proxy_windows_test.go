//go:build windows

package control

import "testing"

// TestFirstProxyTarget pins how a WinINet ProxyServer value is reduced to a bare
// host:port for the startup reconcile's comparison: a plain value passes through,
// and a per-protocol list yields its first target with the "scheme=" prefix
// stripped. The reconcile compares this against our own loopback address, so a
// wrong reduction here would either miss a stale proxy or (worse) match a foreign
// one.
func TestFirstProxyTarget(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain host:port", "127.0.0.1:2080", "127.0.0.1:2080"},
		{"surrounding whitespace", "  127.0.0.1:2080  ", "127.0.0.1:2080"},
		{"per-protocol list", "http=127.0.0.1:2080;https=127.0.0.1:2080", "127.0.0.1:2080"},
		{"single scheme prefix", "http=127.0.0.1:2080", "127.0.0.1:2080"},
		{"remote proxy list", "http=10.0.0.1:8080;https=10.0.0.1:8080", "10.0.0.1:8080"},
		{"empty", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := firstProxyTarget(c.in); got != c.want {
				t.Errorf("firstProxyTarget(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
