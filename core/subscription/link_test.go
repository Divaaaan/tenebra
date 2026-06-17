package subscription

import (
	"errors"
	"reflect"
	"testing"

	"github.com/tenebra-vpn/tenebra/core/model"
)

func TestParseVLESS(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want model.Node
	}{
		{
			name: "reality with vision flow",
			raw:  "vless://11111111-2222-3333-4444-555555555555@example.com:443?type=tcp&security=reality&sni=www.example.com&fp=chrome&pbk=fakepublickey&sid=ab12&spx=%2F&flow=xtls-rprx-vision#Reality%20Node",
			want: model.Node{
				Protocol: model.VLESS,
				Name:     "Reality Node",
				Server:   "example.com",
				Port:     443,
				UUID:     "11111111-2222-3333-4444-555555555555",
				Flow:     "xtls-rprx-vision",
				TLS: &model.TLS{
					Enabled:     true,
					ServerName:  "www.example.com",
					Fingerprint: "chrome",
					Reality:     &model.Reality{PublicKey: "fakepublickey", ShortID: "ab12", SpiderX: "/"},
				},
			},
		},
		{
			name: "ws over tls with alpn",
			raw:  "vless://abc@example.com:8443?type=ws&security=tls&sni=cdn.example.com&host=cdn.example.com&path=%2Fws&alpn=h2,http%2F1.1#ws",
			want: model.Node{
				Protocol:  model.VLESS,
				Name:      "ws",
				Server:    "example.com",
				Port:      8443,
				UUID:      "abc",
				Transport: &model.Transport{Type: "ws", Path: "/ws", Host: "cdn.example.com"},
				TLS:       &model.TLS{Enabled: true, ServerName: "cdn.example.com", ALPN: []string{"h2", "http/1.1"}},
			},
		},
		{
			name: "grpc no tls",
			raw:  "vless://abc@example.com:2053?type=grpc&serviceName=mygrpc&security=none#grpc",
			want: model.Node{
				Protocol:  model.VLESS,
				Name:      "grpc",
				Server:    "example.com",
				Port:      2053,
				UUID:      "abc",
				Transport: &model.Transport{Type: "grpc", ServiceName: "mygrpc"},
			},
		},
		{
			name: "ipv6 host in brackets",
			raw:  "vless://abc@[2001:db8::1]:443?security=tls#v6",
			want: model.Node{
				Protocol: model.VLESS,
				Name:     "v6",
				Server:   "2001:db8::1",
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

func TestParseVLESSErrors(t *testing.T) {
	for _, raw := range []string{
		"vless://@example.com:443",          // missing uuid
		"vless://abc@example.com",           // missing port
		"vless://abc@example.com:notaport",  // non-numeric port
		"vless://abc@example.com:0",         // out-of-range port
		"vless://abc@example.com:70000#big", // out-of-range port
	} {
		if _, err := ParseLink(raw); err == nil {
			t.Errorf("ParseLink(%q) expected error, got nil", raw)
		}
	}
}

func TestParseHysteria2(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want model.Node
	}{
		{
			name: "salamander obfs and insecure",
			raw:  "hysteria2://secretpass@example.com:8444?sni=example.com&insecure=1&obfs=salamander&obfs-password=obfspass&alpn=h3#hy2",
			want: model.Node{
				Protocol: model.Hysteria2,
				Name:     "hy2",
				Server:   "example.com",
				Port:     8444,
				Password: "secretpass",
				TLS:      &model.TLS{Enabled: true, ServerName: "example.com", Insecure: true, ALPN: []string{"h3"}},
				Obfs:     &model.Obfs{Type: "salamander", Password: "obfspass"},
			},
		},
		{
			name: "hy2 alias plain",
			raw:  "hy2://pw@example.com:443#alias",
			want: model.Node{
				Protocol: model.Hysteria2,
				Name:     "alias",
				Server:   "example.com",
				Port:     443,
				Password: "pw",
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

func TestParseTrojan(t *testing.T) {
	got, err := ParseLink("trojan://trojanpass@example.com:443?sni=example.com&type=ws&path=%2Ftj&host=h.example.com&allowInsecure=true&alpn=h2#TJ")
	if err != nil {
		t.Fatalf("ParseLink() error = %v", err)
	}
	want := model.Node{
		Protocol:  model.Trojan,
		Name:      "TJ",
		Server:    "example.com",
		Port:      443,
		Password:  "trojanpass",
		Transport: &model.Transport{Type: "ws", Path: "/tj", Host: "h.example.com"},
		TLS:       &model.TLS{Enabled: true, ServerName: "example.com", Insecure: true, ALPN: []string{"h2"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseLink() =\n %#v\nwant\n %#v", got, want)
	}

	if _, err := ParseLink("trojan://@example.com:443"); err == nil {
		t.Error("expected error for missing trojan password")
	}
}

func TestParseShadowsocks(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want model.Node
	}{
		{
			name: "sip002 base64 userinfo",
			raw:  "ss://Y2hhY2hhMjAtaWV0Zi1wb2x5MTMwNTpzaXAwMDJwYXNz@example.com:8388#sip002",
			want: model.Node{
				Protocol: model.Shadowsocks,
				Name:     "sip002",
				Server:   "example.com",
				Port:     8388,
				Method:   "chacha20-ietf-poly1305",
				Password: "sip002pass",
			},
		},
		{
			name: "sip002 with plugin",
			raw:  "ss://Y2hhY2hhMjAtaWV0Zi1wb2x5MTMwNTpzaXAwMDJwYXNz@example.com:8388?plugin=obfs-local%3Bobfs%3Dhttp#plug",
			want: model.Node{
				Protocol: model.Shadowsocks,
				Name:     "plug",
				Server:   "example.com",
				Port:     8388,
				Method:   "chacha20-ietf-poly1305",
				Password: "sip002pass",
				Extra:    map[string]string{"plugin": "obfs-local;obfs=http"},
			},
		},
		{
			name: "legacy fully-base64 with padding",
			raw:  "ss://YWVzLTI1Ni1nY206bGVnYWN5cGFzc0BleGFtcGxlLmNvbTo4Mzg4#legacy",
			want: model.Node{
				Protocol: model.Shadowsocks,
				Name:     "legacy",
				Server:   "example.com",
				Port:     8388,
				Method:   "aes-256-gcm",
				Password: "legacypass",
			},
		},
		{
			name: "legacy no padding",
			raw:  "ss://YWVzLTI1Ni1nY206bGVnYWN5cGFzc0BleGFtcGxlLmNvbTo4Mzg4#nopad",
			want: model.Node{
				Protocol: model.Shadowsocks,
				Name:     "nopad",
				Server:   "example.com",
				Port:     8388,
				Method:   "aes-256-gcm",
				Password: "legacypass",
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

	if _, err := ParseLink("ss://!!!notbase64!!!#bad"); err == nil {
		t.Error("expected error for malformed ss link")
	}
}

func TestParseVMess(t *testing.T) {
	// base64(JSON) for a ws+tls vmess node, all fake values.
	const link = "vmess://eyJ2IjoiMiIsInBzIjoidm0gbm9kZSIsImFkZCI6ImV4YW1wbGUuY29tIiwicG9ydCI6IjQ0MyIsImlkIjoiMTExMTExMTEtMjIyMi0zMzMzLTQ0NDQtNTU1NTU1NTU1NTU1IiwiYWlkIjoiMCIsInNjeSI6ImF1dG8iLCJuZXQiOiJ3cyIsInR5cGUiOiJub25lIiwiaG9zdCI6ImNkbi5leGFtcGxlLmNvbSIsInBhdGgiOiIvdm0iLCJ0bHMiOiJ0bHMiLCJzbmkiOiJzbmkuZXhhbXBsZS5jb20iLCJhbHBuIjoiaDIsaHR0cC8xLjEifQ=="
	got, err := ParseLink(link)
	if err != nil {
		t.Fatalf("ParseLink() error = %v", err)
	}
	want := model.Node{
		Protocol:  model.VMess,
		Name:      "vm node",
		Server:    "example.com",
		Port:      443,
		UUID:      "11111111-2222-3333-4444-555555555555",
		AlterID:   0,
		Security:  "auto",
		Transport: &model.Transport{Type: "ws", Path: "/vm", Host: "cdn.example.com"},
		TLS:       &model.TLS{Enabled: true, ServerName: "sni.example.com", ALPN: []string{"h2", "http/1.1"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseLink() =\n %#v\nwant\n %#v", got, want)
	}

	if _, err := ParseLink("vmess://%%%notbase64"); err == nil {
		t.Error("expected error for malformed vmess base64")
	}
}

func TestParseVMessNumericFields(t *testing.T) {
	// aid and port encoded as JSON numbers rather than strings, net=tcp, no tls.
	const link = "vmess://eyJ2IjoyLCJwcyI6Im51bSIsImFkZCI6ImV4YW1wbGUuY29tIiwicG9ydCI6NDQzLCJpZCI6IjExMTExMTExLTIyMjItMzMzMy00NDQ0LTU1NTU1NTU1NTU1NSIsImFpZCI6NjQsIm5ldCI6InRjcCIsInRscyI6IiJ9"
	got, err := ParseLink(link)
	if err != nil {
		t.Fatalf("ParseLink() error = %v", err)
	}
	want := model.Node{
		Protocol: model.VMess,
		Name:     "num",
		Server:   "example.com",
		Port:     443,
		UUID:     "11111111-2222-3333-4444-555555555555",
		AlterID:  64,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseLink() =\n %#v\nwant\n %#v", got, want)
	}
}

func TestParseLinkDispatch(t *testing.T) {
	if _, err := ParseLink("amneziawg://whatever"); !errors.Is(err, ErrUnsupportedScheme) {
		t.Errorf("amneziawg: want ErrUnsupportedScheme, got %v", err)
	}
	if _, err := ParseLink("ftp://example.com"); !errors.Is(err, ErrUnsupportedScheme) {
		t.Errorf("ftp: want ErrUnsupportedScheme, got %v", err)
	}
	if _, err := ParseLink("not a url"); err == nil {
		t.Error("expected error for missing scheme")
	}
	if _, err := ParseLink("   "); err == nil {
		t.Error("expected error for empty link")
	}
}
