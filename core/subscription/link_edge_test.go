package subscription

import (
	"encoding/base64"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Divaaaan/tenebra/core/model"
)

// --- VLESS edge cases -------------------------------------------------------

func TestParseVLESSEdge(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want model.Node
	}{
		{
			// An unknown transport type must fall through buildTransport's
			// default branch and keep the lowercased custom type.
			name: "unknown transport type falls through to default",
			raw:  "vless://abc@example.com:443?type=QuIc&path=%2Fq&host=h.example.com&serviceName=svc#custom",
			want: model.Node{
				Protocol:  model.VLESS,
				Name:      "custom",
				Server:    "example.com",
				Port:      443,
				UUID:      "abc",
				Transport: &model.Transport{Type: "quic", Path: "/q", Host: "h.example.com", ServiceName: "svc"},
			},
		},
		{
			// security=reality without a pbk still builds a REALITY block with
			// empty key material rather than dropping it.
			name: "reality without pbk",
			raw:  "vless://abc@example.com:443?security=reality#noreality",
			want: model.Node{
				Protocol: model.VLESS,
				Name:     "noreality",
				Server:   "example.com",
				Port:     443,
				UUID:     "abc",
				TLS:      &model.TLS{Enabled: true, Reality: &model.Reality{}},
			},
		},
		{
			// peer= is the SNI fallback when sni is absent.
			name: "peer used as sni fallback",
			raw:  "vless://abc@example.com:443?security=tls&peer=peer.example.com#peer",
			want: model.Node{
				Protocol: model.VLESS,
				Name:     "peer",
				Server:   "example.com",
				Port:     443,
				UUID:     "abc",
				TLS:      &model.TLS{Enabled: true, ServerName: "peer.example.com"},
			},
		},
		{
			// http/h2 transport maps to type "http".
			name: "http transport",
			raw:  "vless://abc@example.com:443?type=http&path=%2Fhttp&host=h.example.com#http",
			want: model.Node{
				Protocol:  model.VLESS,
				Name:      "http",
				Server:    "example.com",
				Port:      443,
				UUID:      "abc",
				Transport: &model.Transport{Type: "http", Path: "/http", Host: "h.example.com"},
			},
		},
		{
			name: "httpupgrade transport",
			raw:  "vless://abc@example.com:443?type=httpupgrade&path=%2Fhu&host=h.example.com#hu",
			want: model.Node{
				Protocol:  model.VLESS,
				Name:      "hu",
				Server:    "example.com",
				Port:      443,
				UUID:      "abc",
				Transport: &model.Transport{Type: "httpupgrade", Path: "/hu", Host: "h.example.com"},
			},
		},
		{
			// No #fragment: fragmentName returns "" via its empty-fragment path.
			name: "no fragment name empty",
			raw:  "vless://abc@example.com:443?security=tls",
			want: model.Node{
				Protocol: model.VLESS,
				Server:   "example.com",
				Port:     443,
				UUID:     "abc",
				TLS:      &model.TLS{Enabled: true},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseLink(tt.raw)
			if err != nil {
				t.Fatalf("ParseLink() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParseLink() =\n %#v\nwant\n %#v", got, tt.want)
			}
		})
	}
}

func TestParseVLESSEdgeErrors(t *testing.T) {
	for _, raw := range []string{
		"vless://",                        // empty body, no uuid/host
		"vless://abc@ex\x7fample.com:443", // control char makes url.Parse fail
		"vless://abc@example.com:443?security=tls#bad%zzname", // bad fragment escape rejected by url.Parse
	} {
		if _, err := ParseLink(raw); err == nil {
			t.Errorf("ParseLink(%q) expected error, got nil", raw)
		}
	}
}

// --- Hysteria2 edge cases ---------------------------------------------------

func TestParseHysteria2Edge(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want model.Node
	}{
		{
			// user:pass form: the password comes from the colon branch of
			// userinfoSecret, not the username.
			name: "user:pass userinfo",
			raw:  "hysteria2://user:secretpass@example.com:8444#up",
			want: model.Node{
				Protocol: model.Hysteria2,
				Name:     "up",
				Server:   "example.com",
				Port:     8444,
				Password: "secretpass",
				TLS:      &model.TLS{Enabled: true},
			},
		},
		{
			// An obfs value other than salamander is ignored (no Obfs set).
			name: "non-salamander obfs ignored",
			raw:  "hysteria2://pw@example.com:8444?obfs=something&obfs-password=x#noobfs",
			want: model.Node{
				Protocol: model.Hysteria2,
				Name:     "noobfs",
				Server:   "example.com",
				Port:     8444,
				Password: "pw",
				TLS:      &model.TLS{Enabled: true},
			},
		},
		{
			name: "insecure yes truthy",
			raw:  "hysteria2://pw@example.com:8444?insecure=yes#yes",
			want: model.Node{
				Protocol: model.Hysteria2,
				Name:     "yes",
				Server:   "example.com",
				Port:     8444,
				Password: "pw",
				TLS:      &model.TLS{Enabled: true, Insecure: true},
			},
		},
		{
			name: "insecure on truthy",
			raw:  "hysteria2://pw@example.com:8444?insecure=on#on",
			want: model.Node{
				Protocol: model.Hysteria2,
				Name:     "on",
				Server:   "example.com",
				Port:     8444,
				Password: "pw",
				TLS:      &model.TLS{Enabled: true, Insecure: true},
			},
		},
		{
			name: "insecure true truthy",
			raw:  "hysteria2://pw@example.com:8444?insecure=true#tr",
			want: model.Node{
				Protocol: model.Hysteria2,
				Name:     "tr",
				Server:   "example.com",
				Port:     8444,
				Password: "pw",
				TLS:      &model.TLS{Enabled: true, Insecure: true},
			},
		},
		{
			name: "insecure false falsy",
			raw:  "hysteria2://pw@example.com:8444?insecure=false#fa",
			want: model.Node{
				Protocol: model.Hysteria2,
				Name:     "fa",
				Server:   "example.com",
				Port:     8444,
				Password: "pw",
				TLS:      &model.TLS{Enabled: true, Insecure: false},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseLink(tt.raw)
			if err != nil {
				t.Fatalf("ParseLink() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParseLink() =\n %#v\nwant\n %#v", got, tt.want)
			}
		})
	}
}

func TestParseHysteria2EdgeErrors(t *testing.T) {
	for _, raw := range []string{
		"hysteria2://pw@example.com",        // missing port
		"hysteria2://pw@example.com:notnum", // bad host:port
	} {
		if _, err := ParseLink(raw); err == nil {
			t.Errorf("ParseLink(%q) expected error, got nil", raw)
		}
	}
}

// --- Shadowsocks edge cases -------------------------------------------------

func TestParseShadowsocksEdge(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want model.Node
	}{
		{
			// SIP002 userinfo that is NOT valid base64 but already holds
			// method:password: decodeBase64 fails and the raw value is used.
			name: "sip002 non-base64 userinfo fallback",
			raw:  "ss://aes-256-gcm:plainpass@example.com:8388#raw",
			want: model.Node{
				Protocol: model.Shadowsocks,
				Name:     "raw",
				Server:   "example.com",
				Port:     8388,
				Method:   "aes-256-gcm",
				Password: "plainpass",
			},
		},
		{
			// IPv6 host in SIP002 form (bracketed literal).
			name: "sip002 ipv6 host",
			raw:  "ss://Y2hhY2hhMjAtaWV0Zi1wb2x5MTMwNTpzaXAwMDJwYXNz@[2001:db8::1]:8388#v6",
			want: model.Node{
				Protocol: model.Shadowsocks,
				Name:     "v6",
				Server:   "2001:db8::1",
				Port:     8388,
				Method:   "chacha20-ietf-poly1305",
				Password: "sip002pass",
			},
		},
		{
			// Legacy fully-base64 form with the @host:port inside the blob.
			name: "legacy fully base64",
			raw:  "ss://YWVzLTI1Ni1nY206bGVnYWN5cGFzczJAZXhhbXBsZS5jb206ODM4OA==#leg",
			want: model.Node{
				Protocol: model.Shadowsocks,
				Name:     "leg",
				Server:   "example.com",
				Port:     8388,
				Method:   "aes-256-gcm",
				Password: "legacypass2",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseLink(tt.raw)
			if err != nil {
				t.Fatalf("ParseLink() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParseLink() =\n %#v\nwant\n %#v", got, tt.want)
			}
		})
	}
}

func TestParseShadowsocksEdgeErrors(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{
			// SIP002 raw userinfo with no colon (and not base64) -> malformed.
			name: "sip002 no colon",
			raw:  "ss://plainmethodnopassword@example.com:8388#x",
		},
		{
			// Legacy blob decodes to creds with no colon -> malformed.
			name: "legacy no colon in creds",
			raw:  "ss://YWVzMjU2Z2NtbGVnYWN5cGFzc0BleGFtcGxlLmNvbTo4Mzg4#x",
		},
		{
			// Legacy blob decodes to ":pass@host:port" -> empty method.
			name: "legacy empty method",
			raw:  "ss://OmxlZ2FjeXBhc3NAZXhhbXBsZS5jb206ODM4OA==#x",
		},
		{
			// Legacy blob with no @ separating creds from authority.
			name: "legacy no at",
			raw:  "ss://YWVzLTI1Ni1nY206bGVnYWN5cGFzcw==#x",
		},
		{
			// Body that is neither valid base64 nor SIP002 (no @).
			name: "legacy base64 failure",
			raw:  "ss://!!!notbase64!!!#x",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ParseLink(tt.raw); err == nil {
				t.Errorf("ParseLink(%q) expected error, got nil", tt.raw)
			}
		})
	}
}

func TestParseShadowsocksPluginQuery(t *testing.T) {
	got, err := ParseLink("ss://aes-256-gcm:plainpass@example.com:8388?plugin=v2ray-plugin%3Btls#pl")
	if err != nil {
		t.Fatalf("ParseLink() error = %v", err)
	}
	want := model.Node{
		Protocol: model.Shadowsocks,
		Name:     "pl",
		Server:   "example.com",
		Port:     8388,
		Method:   "aes-256-gcm",
		Password: "plainpass",
		Extra:    map[string]string{"plugin": "v2ray-plugin;tls"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseLink() =\n %#v\nwant\n %#v", got, want)
	}
}

// --- Trojan edge cases ------------------------------------------------------

func TestParseTrojanEdge(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want model.Node
	}{
		{
			// No query string: TLS is on by default and there is no transport.
			name: "no query defaults tls on",
			raw:  "trojan://pw@example.com:443#plain",
			want: model.Node{
				Protocol: model.Trojan,
				Name:     "plain",
				Server:   "example.com",
				Port:     443,
				Password: "pw",
				TLS:      &model.TLS{Enabled: true},
			},
		},
		{
			name: "ipv6 host",
			raw:  "trojan://pw@[2001:db8::1]:443#v6",
			want: model.Node{
				Protocol: model.Trojan,
				Name:     "v6",
				Server:   "2001:db8::1",
				Port:     443,
				Password: "pw",
				TLS:      &model.TLS{Enabled: true},
			},
		},
		{
			name: "grpc transport",
			raw:  "trojan://pw@example.com:443?type=grpc&serviceName=tjgrpc#g",
			want: model.Node{
				Protocol:  model.Trojan,
				Name:      "g",
				Server:    "example.com",
				Port:      443,
				Password:  "pw",
				Transport: &model.Transport{Type: "grpc", ServiceName: "tjgrpc"},
				TLS:       &model.TLS{Enabled: true},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseLink(tt.raw)
			if err != nil {
				t.Fatalf("ParseLink() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParseLink() =\n %#v\nwant\n %#v", got, tt.want)
			}
		})
	}
}

func TestParseTrojanEdgeErrors(t *testing.T) {
	for _, raw := range []string{
		"trojan://pw@ex\x7fample.com:443", // control char makes url.Parse fail
		"trojan://pw@example.com",         // missing port
	} {
		if _, err := ParseLink(raw); err == nil {
			t.Errorf("ParseLink(%q) expected error, got nil", raw)
		}
	}
}

// --- VMess edge cases -------------------------------------------------------

func TestParseVMessEdge(t *testing.T) {
	tests := []struct {
		name string
		b64  string
		want model.Node
	}{
		{
			// aid as a JSON float (number) decodes via the float64 branch.
			name: "aid float",
			b64:  "eyJwcyI6ImFpZGZsb2F0IiwiYWRkIjoiZXhhbXBsZS5jb20iLCJwb3J0Ijo0NDMsImlkIjoiMTExMTExMTEtMjIyMi0zMzMzLTQ0NDQtNTU1NTU1NTU1NTU1IiwiYWlkIjoyfQ==",
			want: model.Node{
				Protocol: model.VMess,
				Name:     "aidfloat",
				Server:   "example.com",
				Port:     443,
				UUID:     "11111111-2222-3333-4444-555555555555",
				AlterID:  2,
			},
		},
		{
			// aid as a string decodes via the string branch.
			name: "aid string",
			b64:  "eyJwcyI6ImFpZHN0ciIsImFkZCI6ImV4YW1wbGUuY29tIiwicG9ydCI6NDQzLCJpZCI6IjExMTExMTExLTIyMjItMzMzMy00NDQ0LTU1NTU1NTU1NTU1NSIsImFpZCI6IjMifQ==",
			want: model.Node{
				Protocol: model.VMess,
				Name:     "aidstr",
				Server:   "example.com",
				Port:     443,
				UUID:     "11111111-2222-3333-4444-555555555555",
				AlterID:  3,
			},
		},
		{
			name: "net httpupgrade",
			b64:  "eyJwcyI6Imh1IiwiYWRkIjoiZXhhbXBsZS5jb20iLCJwb3J0Ijo0NDMsImlkIjoiMTExMTExMTEtMjIyMi0zMzMzLTQ0NDQtNTU1NTU1NTU1NTU1IiwibmV0IjoiaHR0cHVwZ3JhZGUiLCJwYXRoIjoiL2h1IiwiaG9zdCI6ImguZXhhbXBsZS5jb20ifQ==",
			want: model.Node{
				Protocol:  model.VMess,
				Name:      "hu",
				Server:    "example.com",
				Port:      443,
				UUID:      "11111111-2222-3333-4444-555555555555",
				Transport: &model.Transport{Type: "httpupgrade", Path: "/hu", Host: "h.example.com"},
			},
		},
		{
			name: "net h2",
			b64:  "eyJwcyI6ImgybmV0IiwiYWRkIjoiZXhhbXBsZS5jb20iLCJwb3J0Ijo0NDMsImlkIjoiMTExMTExMTEtMjIyMi0zMzMzLTQ0NDQtNTU1NTU1NTU1NTU1IiwibmV0IjoiaDIiLCJwYXRoIjoiL2gyIiwiaG9zdCI6ImgyLmV4YW1wbGUuY29tIn0=",
			want: model.Node{
				Protocol:  model.VMess,
				Name:      "h2net",
				Server:    "example.com",
				Port:      443,
				UUID:      "11111111-2222-3333-4444-555555555555",
				Transport: &model.Transport{Type: "http", Path: "/h2", Host: "h2.example.com"},
			},
		},
		{
			// tls is truthy but sni is empty -> ServerName falls back to host.
			name: "tls sni empty falls back to host",
			b64:  "eyJwcyI6InRsc2ZiIiwiYWRkIjoiZXhhbXBsZS5jb20iLCJwb3J0Ijo0NDMsImlkIjoiMTExMTExMTEtMjIyMi0zMzMzLTQ0NDQtNTU1NTU1NTU1NTU1IiwibmV0Ijoid3MiLCJwYXRoIjoiL3ciLCJob3N0IjoiZmFsbGJhY2suZXhhbXBsZS5jb20iLCJ0bHMiOiJ0bHMiLCJzbmkiOiIifQ==",
			want: model.Node{
				Protocol:  model.VMess,
				Name:      "tlsfb",
				Server:    "example.com",
				Port:      443,
				UUID:      "11111111-2222-3333-4444-555555555555",
				Transport: &model.Transport{Type: "ws", Path: "/w", Host: "fallback.example.com"},
				TLS:       &model.TLS{Enabled: true, ServerName: "fallback.example.com"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseLink("vmess://" + tt.b64)
			if err != nil {
				t.Fatalf("ParseLink() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParseLink() =\n %#v\nwant\n %#v", got, tt.want)
			}
		})
	}
}

func TestParseVMessEdgeErrors(t *testing.T) {
	tests := []struct {
		name string
		b64  string
	}{
		{
			name: "missing add",
			b64:  "eyJ2IjoiMiIsInBzIjoibm9hZGQiLCJhZGQiOiIiLCJwb3J0IjoiNDQzIiwiaWQiOiIxMTExMTExMS0yMjIyLTMzMzMtNDQ0NC01NTU1NTU1NTU1NTUifQ==",
		},
		{
			name: "missing id",
			b64:  "eyJ2IjoiMiIsInBzIjoibm9pZCIsImFkZCI6ImV4YW1wbGUuY29tIiwicG9ydCI6IjQ0MyIsImlkIjoiIn0=",
		},
		{
			name: "port zero",
			b64:  "eyJwcyI6InAwIiwiYWRkIjoiZXhhbXBsZS5jb20iLCJwb3J0IjowLCJpZCI6IjExMTExMTExLTIyMjItMzMzMy00NDQ0LTU1NTU1NTU1NTU1NSJ9",
		},
		{
			name: "port 70000",
			b64:  "eyJwcyI6InA3MDAwMCIsImFkZCI6ImV4YW1wbGUuY29tIiwicG9ydCI6NzAwMDAsImlkIjoiMTExMTExMTEtMjIyMi0zMzMzLTQ0NDQtNTU1NTU1NTU1NTU1In0=",
		},
		{
			name: "port nonnumeric string",
			b64:  "eyJwcyI6InBzdHIiLCJhZGQiOiJleGFtcGxlLmNvbSIsInBvcnQiOiJub3RhcG9ydCIsImlkIjoiMTExMTExMTEtMjIyMi0zMzMzLTQ0NDQtNTU1NTU1NTU1NTU1In0=",
		},
		{
			name: "malformed json after decode",
			b64:  "eyJwcyI6ImJhZCIsImFkZCI6ImV4YW1wbGUuY29tIiAicG9ydCI6NDQzfQ==",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ParseLink("vmess://" + tt.b64); err == nil {
				t.Errorf("ParseLink(vmess %s) expected error, got nil", tt.name)
			}
		})
	}

	// Base64 failure (payload is not decodable at all).
	if _, err := ParseLink("vmess://@@@notbase64@@@"); err == nil {
		t.Error("expected error for vmess base64 failure")
	}
}

// --- hostPort directly ------------------------------------------------------

func TestHostPort(t *testing.T) {
	t.Run("ok ipv6", func(t *testing.T) {
		host, port, err := hostPort("[::1]:443")
		if err != nil {
			t.Fatalf("hostPort() error = %v", err)
		}
		if host != "::1" || port != 443 {
			t.Errorf("hostPort() = %q,%d want ::1,443", host, port)
		}
	})

	for _, authority := range []string{
		"",           // empty authority
		"host",       // no port
		"host:0",     // port out of range (low)
		"host:99999", // port out of range (high)
		"[]:443",     // empty host
	} {
		t.Run("err "+authority, func(t *testing.T) {
			if _, _, err := hostPort(authority); err == nil {
				t.Errorf("hostPort(%q) expected error, got nil", authority)
			}
		})
	}
}

// --- decodeBase64 alphabet/whitespace edge cases ----------------------------

func TestDecodeBase64EdgeForms(t *testing.T) {
	const want = "method:password@example.com:443"
	std := base64.StdEncoding.EncodeToString([]byte(want))

	t.Run("embedded whitespace stripped", func(t *testing.T) {
		// Inject newlines, spaces and tabs mid-string; they must be removed.
		noisy := std[:6] + "\n" + std[6:12] + " \t\r\n" + std[12:]
		got, err := decodeBase64(noisy)
		if err != nil {
			t.Fatalf("decodeBase64() error = %v", err)
		}
		if string(got) != want {
			t.Errorf("decodeBase64() = %q, want %q", got, want)
		}
	})

	t.Run("padding-less", func(t *testing.T) {
		got, err := decodeBase64(base64.RawStdEncoding.EncodeToString([]byte(want)))
		if err != nil {
			t.Fatalf("decodeBase64() error = %v", err)
		}
		if string(got) != want {
			t.Errorf("decodeBase64() = %q, want %q", got, want)
		}
	})

	t.Run("url-safe alphabet with - and _", func(t *testing.T) {
		// {251,239,190} -> std "++++" -> url-safe "----"; also build a longer
		// payload exercising '_' (std '/').
		urlSafe := base64.RawURLEncoding.EncodeToString([]byte{0xfb, 0xff, 0xbf})
		if !strings.ContainsAny(urlSafe, "-_") {
			t.Fatalf("test fixture %q lacks url-safe chars", urlSafe)
		}
		got, err := decodeBase64(urlSafe)
		if err != nil {
			t.Fatalf("decodeBase64(%q) error = %v", urlSafe, err)
		}
		if !reflect.DeepEqual(got, []byte{0xfb, 0xff, 0xbf}) {
			t.Errorf("decodeBase64(%q) = %v", urlSafe, got)
		}
	})
}

// --- ParseSubscription edge cases -------------------------------------------

func TestParseSubscriptionPlainNoBase64(t *testing.T) {
	// A plaintext body that is not base64 at all.
	body := []byte("vless://abc@example.com:443?security=tls#a\n" +
		"trojan://pw@example.com:443#t\n")
	nodes, skipped, err := ParseSubscription(body)
	if err != nil {
		t.Fatalf("ParseSubscription() error = %v", err)
	}
	if len(nodes) != 2 || skipped != 0 {
		t.Fatalf("got nodes=%d skipped=%d, want 2/0", len(nodes), skipped)
	}
}

func TestParseSubscriptionBase64NonLink(t *testing.T) {
	// Valid base64 that decodes to bytes WITHOUT "://": looksLikeLinks is
	// false, so the body is treated as plaintext (which then has no links).
	plain := "just some random text without any scheme"
	body := []byte(base64.StdEncoding.EncodeToString([]byte(plain)))
	nodes, skipped, err := ParseSubscription(body)
	if err != nil {
		t.Fatalf("ParseSubscription() error = %v", err)
	}
	// The encoded base64 string itself is one non-comment line that is not a
	// link, so it counts as a single skip.
	if len(nodes) != 0 {
		t.Errorf("len(nodes) = %d, want 0", len(nodes))
	}
	if skipped != 1 {
		t.Errorf("skipped = %d, want 1", skipped)
	}
}

func TestParseSubscriptionCommentsAndMixed(t *testing.T) {
	body := []byte("// leading comment\n" +
		"# hash comment\n" +
		"   \n" +
		"vless://abc@example.com:443?security=tls#good\n" +
		"ss://!!!notbase64!!!#bad1\n" +
		"vmess://@@@notbase64@@@\n" +
		"hy2://pw@example.com:8444#good2\n")
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

func TestParseSubscriptionWhitespaceOnly(t *testing.T) {
	nodes, skipped, err := ParseSubscription([]byte("\n  \t \r\n   "))
	if err != nil {
		t.Fatalf("ParseSubscription() error = %v", err)
	}
	if len(nodes) != 0 || skipped != 0 {
		t.Errorf("got nodes=%d skipped=%d, want 0/0", len(nodes), skipped)
	}
}

// --- ParseUserInfo edge cases -----------------------------------------------

func TestParseUserInfoEdge(t *testing.T) {
	t.Run("garbage no equals", func(t *testing.T) {
		got, err := ParseUserInfo("this has no separators at all")
		if err != nil {
			t.Fatalf("ParseUserInfo() error = %v", err)
		}
		if got != (UserInfo{}) {
			t.Errorf("got %#v, want zero UserInfo", got)
		}
	})

	t.Run("negative numbers parse", func(t *testing.T) {
		got, err := ParseUserInfo("upload=-5; download=-10; total=-1")
		if err != nil {
			t.Fatalf("ParseUserInfo() error = %v", err)
		}
		if got.Upload != -5 || got.Download != -10 || got.Total != -1 {
			t.Errorf("got up/dn/total = %d/%d/%d, want -5/-10/-1",
				got.Upload, got.Download, got.Total)
		}
		if !got.Expire.IsZero() {
			t.Errorf("expire = %v, want zero", got.Expire)
		}
	})

	t.Run("expire non-numeric stays zero", func(t *testing.T) {
		got, err := ParseUserInfo("expire=soon")
		if err != nil {
			t.Fatalf("ParseUserInfo() error = %v", err)
		}
		if !got.Expire.IsZero() {
			t.Errorf("expire = %v, want zero", got.Expire)
		}
	})

	t.Run("case-insensitive keys", func(t *testing.T) {
		got, err := ParseUserInfo("UPLOAD=7; Download=8; ToTaL=9; EXPIRE=1700000000")
		if err != nil {
			t.Fatalf("ParseUserInfo() error = %v", err)
		}
		if got.Upload != 7 || got.Download != 8 || got.Total != 9 {
			t.Errorf("got up/dn/total = %d/%d/%d, want 7/8/9",
				got.Upload, got.Download, got.Total)
		}
		if !got.Expire.Equal(time.Unix(1700000000, 0)) {
			t.Errorf("expire = %v, want %v", got.Expire, time.Unix(1700000000, 0))
		}
	})
}

// --- ParseLink dispatch edge cases ------------------------------------------

func TestParseLinkUnsupportedSchemes(t *testing.T) {
	for _, raw := range []string{
		"wireguard://whatever",
		"awg://whatever",
		"amneziawg://whatever",
		"sftp://example.com",
	} {
		if _, err := ParseLink(raw); !errors.Is(err, ErrUnsupportedScheme) {
			t.Errorf("ParseLink(%q): want ErrUnsupportedScheme, got %v", raw, err)
		}
	}
}
