package subscription

import "testing"

// A representative managed key: exactly 32 lowercase hex.
const managedKey = "00112233445566778899aabbccddeeff"

func TestDetectManagedRecognisesOperatorSubscriptions(t *testing.T) {
	cases := []struct {
		name string
		url  string
	}{
		{"vpsxd host", "https://vpsxd.pro/sub/vpn_777_deadbeef/" + managedKey},
		{"vpnxd host", "https://vpnxd.pro/sub/vpn_1_0a1b2c3d/" + managedKey},
		{"chatakfix host", "https://chatakfix.online/sub/vpn_42_ffffffff/" + managedKey},
		{"trailing format segment", "https://vpsxd.pro/sub/vpn_9_abcdef01/" + managedKey + "/clash"},
		{"trailing slash", "https://vpsxd.pro/sub/vpn_9_abcdef01/" + managedKey + "/"},
		{"host case-insensitive", "https://VPSXD.PRO/sub/vpn_9_abcdef01/" + managedKey},
		{"host with port", "https://vpsxd.pro:8443/sub/vpn_9_abcdef01/" + managedKey},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !DetectManaged(tc.url) {
				t.Fatalf("DetectManaged(%q) = false, want true", tc.url)
			}
		})
	}
}

func TestDetectManagedRejectsEverythingElse(t *testing.T) {
	cases := []struct {
		name string
		url  string
	}{
		{"unlisted host", "https://example.com/sub/vpn_777_deadbeef/" + managedKey},
		{"allowlisted host, wrong path", "https://vpsxd.pro/other/vpn_777_deadbeef/" + managedKey},
		{"missing sub prefix", "https://vpsxd.pro/vpn_777_deadbeef/" + managedKey},
		{"key too short", "https://vpsxd.pro/sub/vpn_777_deadbeef/00112233"},
		{"key too long", "https://vpsxd.pro/sub/vpn_777_deadbeef/" + managedKey + "ff"},
		{"key non-hex", "https://vpsxd.pro/sub/vpn_777_deadbeef/zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"},
		{"hash not 8 hex", "https://vpsxd.pro/sub/vpn_777_dead/" + managedKey},
		{"non-numeric id", "https://vpsxd.pro/sub/vpn_abc_deadbeef/" + managedKey},
		{"empty", ""},
		{"no host (manual link)", "vless://uuid@host:443"},
		{"not a url", "!!!"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if DetectManaged(tc.url) {
				t.Fatalf("DetectManaged(%q) = true, want false", tc.url)
			}
		})
	}
}

func TestEntitlementTargetExtractsOriginAndKey(t *testing.T) {
	origin, key, ok := EntitlementTarget("https://vpsxd.pro/sub/vpn_777_deadbeef/" + managedKey + "/clash")
	if !ok {
		t.Fatal("EntitlementTarget ok = false, want true")
	}
	if origin != "https://vpsxd.pro" {
		t.Errorf("origin = %q, want %q", origin, "https://vpsxd.pro")
	}
	if key != managedKey {
		t.Errorf("key = %q, want %q", key, managedKey)
	}
}

func TestEntitlementTargetPreservesPort(t *testing.T) {
	origin, _, ok := EntitlementTarget("https://vpnxd.pro:8443/sub/vpn_5_0a1b2c3d/" + managedKey)
	if !ok {
		t.Fatal("EntitlementTarget ok = false, want true")
	}
	if origin != "https://vpnxd.pro:8443" {
		t.Errorf("origin = %q, want %q", origin, "https://vpnxd.pro:8443")
	}
}

func TestEntitlementTargetRejectsNonManaged(t *testing.T) {
	if _, _, ok := EntitlementTarget("https://example.com/sub/vpn_1_deadbeef/" + managedKey); ok {
		t.Fatal("EntitlementTarget ok = true for an unlisted host, want false")
	}
}
