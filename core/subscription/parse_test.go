package subscription

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func TestParseSubscriptionBase64(t *testing.T) {
	links := strings.Join([]string{
		"vless://abc@example.com:443?security=tls#a",
		"hy2://pw@example.com:8444#b",
		"ss://Y2hhY2hhMjAtaWV0Zi1wb2x5MTMwNTpzaXAwMDJwYXNz@example.com:8388#c",
	}, "\n")
	body := []byte(base64.StdEncoding.EncodeToString([]byte(links)))

	nodes, skipped, err := ParseSubscription(body)
	if err != nil {
		t.Fatalf("ParseSubscription() error = %v", err)
	}
	if skipped != 0 {
		t.Errorf("skipped = %d, want 0", skipped)
	}
	if len(nodes) != 3 {
		t.Fatalf("len(nodes) = %d, want 3", len(nodes))
	}
	if nodes[0].Name != "a" || nodes[1].Name != "b" || nodes[2].Name != "c" {
		t.Errorf("unexpected node names: %q %q %q", nodes[0].Name, nodes[1].Name, nodes[2].Name)
	}
}

func TestParseSubscriptionURLSafeBase64(t *testing.T) {
	links := "vless://abc@example.com:443?security=tls#only"
	// RawURLEncoding yields a url-safe, unpadded body.
	body := []byte(base64.RawURLEncoding.EncodeToString([]byte(links)))
	nodes, _, err := ParseSubscription(body)
	if err != nil {
		t.Fatalf("ParseSubscription() error = %v", err)
	}
	if len(nodes) != 1 || nodes[0].Name != "only" {
		t.Fatalf("got %d nodes, want 1 named 'only'", len(nodes))
	}
}

func TestParseSubscriptionPlaintext(t *testing.T) {
	body := []byte("# header comment\n" +
		"vless://abc@example.com:443?security=tls#a\n" +
		"\n" +
		"// another comment\n" +
		"this-is-not-a-link\n" +
		"vmess://%%%bad\n" +
		"trojan://pw@example.com:443#t\n")

	nodes, skipped, err := ParseSubscription(body)
	if err != nil {
		t.Fatalf("ParseSubscription() error = %v", err)
	}
	if len(nodes) != 2 {
		t.Errorf("len(nodes) = %d, want 2", len(nodes))
	}
	if skipped != 2 {
		t.Errorf("skipped = %d, want 2", skipped)
	}
}

func TestParseSubscriptionEmpty(t *testing.T) {
	nodes, skipped, err := ParseSubscription([]byte("   \n  "))
	if err != nil {
		t.Fatalf("ParseSubscription() error = %v", err)
	}
	if len(nodes) != 0 || skipped != 0 {
		t.Errorf("got nodes=%d skipped=%d, want 0/0", len(nodes), skipped)
	}
}

func TestParseUserInfo(t *testing.T) {
	tests := []struct {
		name       string
		header     string
		up, dn, tt int64
		expire     time.Time
	}{
		{
			name:   "full header",
			header: "upload=100; download=200; total=300; expire=1700000000",
			up:     100, dn: 200, tt: 300,
			expire: time.Unix(1700000000, 0),
		},
		{
			name:   "no expire",
			header: "upload=0; download=5; total=10",
			up:     0, dn: 5, tt: 10,
			expire: time.Time{},
		},
		{
			name:   "spacing variations and unknown key",
			header: "upload=1;download=2 ; total = 3 ; foo=bar",
			up:     1, dn: 2, tt: 3,
			expire: time.Time{},
		},
		{
			name:   "expire zero stays zero",
			header: "expire=0",
			expire: time.Time{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseUserInfo(tc.header)
			if err != nil {
				t.Fatalf("ParseUserInfo() error = %v", err)
			}
			if got.Upload != tc.up || got.Download != tc.dn || got.Total != tc.tt {
				t.Errorf("got up/dn/total = %d/%d/%d, want %d/%d/%d",
					got.Upload, got.Download, got.Total, tc.up, tc.dn, tc.tt)
			}
			if !got.Expire.Equal(tc.expire) {
				t.Errorf("expire = %v, want %v", got.Expire, tc.expire)
			}
		})
	}
}

func TestDecodeBase64Variants(t *testing.T) {
	const want = "method:password@example.com:443"
	for _, enc := range []string{
		base64.StdEncoding.EncodeToString([]byte(want)),
		base64.RawStdEncoding.EncodeToString([]byte(want)),
		base64.URLEncoding.EncodeToString([]byte(want)),
		base64.RawURLEncoding.EncodeToString([]byte(want)),
	} {
		got, err := decodeBase64(enc)
		if err != nil {
			t.Fatalf("decodeBase64(%q) error = %v", enc, err)
		}
		if string(got) != want {
			t.Errorf("decodeBase64(%q) = %q, want %q", enc, got, want)
		}
	}
}
