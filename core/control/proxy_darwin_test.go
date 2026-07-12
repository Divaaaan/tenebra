//go:build darwin

package control

import "testing"

// TestParseGetWebProxy pins how `networksetup -getwebproxy` output is read into a
// proxyState: the Enabled/Server/Port lines fold into an enabled flag and a
// host:port, and a disabled or serverless block yields an empty target so the
// startup reconcile compares against nothing rather than a half-parsed address.
func TestParseGetWebProxy(t *testing.T) {
	cases := []struct {
		name        string
		in          string
		wantEnabled bool
		wantServer  string
	}{
		{
			name:        "enabled loopback",
			in:          "Enabled: Yes\nServer: 127.0.0.1\nPort: 2080\nAuthenticated Proxy Enabled: 0\n",
			wantEnabled: true,
			wantServer:  "127.0.0.1:2080",
		},
		{
			name:        "disabled",
			in:          "Enabled: No\nServer: 127.0.0.1\nPort: 2080\n",
			wantEnabled: false,
			wantServer:  "127.0.0.1:2080",
		},
		{
			name:        "no server configured",
			in:          "Enabled: No\nServer:\nPort: 0\n",
			wantEnabled: false,
			wantServer:  "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := parseGetWebProxy(c.in)
			if st.Enabled != c.wantEnabled || st.Server != c.wantServer {
				t.Errorf("parseGetWebProxy(%q) = {%v %q}, want {%v %q}",
					c.in, st.Enabled, st.Server, c.wantEnabled, c.wantServer)
			}
		})
	}
}

// TestServiceForDevice pins mapping a default-route device (e.g. en0) to its
// network service name in `networksetup -listnetworkserviceorder` output — the
// step that lets the guard set the proxy on the right service (Wi-Fi vs Ethernet).
func TestServiceForDevice(t *testing.T) {
	order := "An asterisk (*) denotes that a network service is disabled.\n" +
		"(1) Wi-Fi\n" +
		"(Hardware Port: Wi-Fi, Device: en0)\n" +
		"\n" +
		"(2) Thunderbolt Bridge\n" +
		"(Hardware Port: Thunderbolt Bridge, Device: bridge0)\n"

	cases := []struct {
		dev  string
		want string
	}{
		{"en0", "Wi-Fi"},
		{"bridge0", "Thunderbolt Bridge"},
		{"en9", ""}, // unknown device -> no service
	}
	for _, c := range cases {
		t.Run(c.dev, func(t *testing.T) {
			if got := serviceForDevice(order, c.dev); got != c.want {
				t.Errorf("serviceForDevice(dev=%q) = %q, want %q", c.dev, got, c.want)
			}
		})
	}
}
