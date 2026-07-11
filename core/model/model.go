// Package model holds the types shared across the core: the normalized
// representation of a proxy server and the pieces of its configuration.
package model

// Protocol is the proxy protocol a Node speaks.
type Protocol string

const (
	VLESS       Protocol = "vless"
	Hysteria2   Protocol = "hysteria2"
	Shadowsocks Protocol = "shadowsocks"
	Trojan      Protocol = "trojan"
	VMess       Protocol = "vmess"
	AmneziaWG   Protocol = "amneziawg"
)

// Node is a single proxy server, normalized across protocols. Subscription
// parsers produce Nodes; the sing-box config generator consumes them. A
// protocol field left at its zero value is omitted from the generated outbound.
type Node struct {
	Protocol Protocol `json:"protocol"`
	Name     string   `json:"name"`
	Server   string   `json:"server"`
	Port     int      `json:"port"`

	UUID     string `json:"uuid,omitempty"`     // vless, vmess
	Password string `json:"password,omitempty"` // hysteria2, trojan, shadowsocks
	Method   string `json:"method,omitempty"`   // shadowsocks cipher

	Flow     string `json:"flow,omitempty"`     // vless, e.g. xtls-rprx-vision
	Security string `json:"security,omitempty"` // vmess cipher (auto/none/...)
	AlterID  int    `json:"alter_id,omitempty"` // vmess

	TLS       *TLS       `json:"tls,omitempty"`
	Transport *Transport `json:"transport,omitempty"`
	Obfs      *Obfs      `json:"obfs,omitempty"`      // hysteria2
	WireGuard *WireGuard `json:"wireguard,omitempty"` // amneziawg

	// Extra keeps query parameters we parsed but do not model explicitly, so a
	// round-trip never silently drops information.
	Extra map[string]string `json:"extra,omitempty"`
}

// TLS describes the transport security layer.
type TLS struct {
	Enabled     bool     `json:"enabled"`
	ServerName  string   `json:"server_name,omitempty"` // SNI
	Insecure    bool     `json:"insecure,omitempty"`
	ALPN        []string `json:"alpn,omitempty"`
	Fingerprint string   `json:"fingerprint,omitempty"` // uTLS, e.g. chrome
	// Fragment splits the TLS ClientHello across multiple TCP segments so DPI that
	// keys on the plaintext SNI in a single first packet cannot match it. It maps to
	// the sing-box outbound tls.fragment option (the fallback delay is fixed by the
	// builder). Meaningful only over TCP-based TLS; inert on QUIC protocols.
	Fragment bool     `json:"fragment,omitempty"`
	Reality  *Reality `json:"reality,omitempty"`
}

// Reality holds REALITY handshake parameters (VLESS).
type Reality struct {
	PublicKey string `json:"public_key"`
	ShortID   string `json:"short_id,omitempty"`
	SpiderX   string `json:"spider_x,omitempty"`
}

// Transport is the stream transport. An empty Type means raw TCP.
type Transport struct {
	Type        string `json:"type"`                   // ws, grpc, http, httpupgrade
	Path        string `json:"path,omitempty"`         // ws, http, httpupgrade
	Host        string `json:"host,omitempty"`         // Host header
	ServiceName string `json:"service_name,omitempty"` // grpc
}

// Obfs is Hysteria2 obfuscation (Salamander).
type Obfs struct {
	Type     string `json:"type"` // salamander
	Password string `json:"password,omitempty"`
}

// WireGuard carries (Amnezia)WG key material and the AmneziaWG obfuscation
// knobs. Jc/Jmin/Jmax/S1/S2/H1-H4 are zero for plain WireGuard.
type WireGuard struct {
	PrivateKey    string   `json:"private_key"`
	PeerPublicKey string   `json:"peer_public_key"`
	PreSharedKey  string   `json:"pre_shared_key,omitempty"`
	LocalAddress  []string `json:"local_address,omitempty"`

	Jc   int   `json:"jc,omitempty"`
	Jmin int   `json:"jmin,omitempty"`
	Jmax int   `json:"jmax,omitempty"`
	S1   int   `json:"s1,omitempty"`
	S2   int   `json:"s2,omitempty"`
	H1   int64 `json:"h1,omitempty"`
	H2   int64 `json:"h2,omitempty"`
	H3   int64 `json:"h3,omitempty"`
	H4   int64 `json:"h4,omitempty"`
}
