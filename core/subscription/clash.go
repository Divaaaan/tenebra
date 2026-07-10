package subscription

import (
	"strconv"
	"strings"

	"github.com/Divaaaan/tenebra/core/model"
)

// clash.go maps a decoded Clash/Mihomo proxy entry onto the normalized
// model.Node, the same target the share-link parsers produce. Clash uses
// different field names than the share links (cipher vs method, servername vs
// sni, skip-cert-verify vs insecure, reality-opts.public-key vs pbk, …), so each
// protocol is translated field by field. A proxy whose type Tenebra does not
// support, or that is missing the fields its protocol needs, yields ok=false and
// is counted as skipped exactly like an unparseable link.

// clashProxyToNode routes one proxy map to the mapper for its type. A missing or
// invalid endpoint (server/port) fails every type up front, since even a
// WireGuard peer needs a reachable host:port.
func clashProxyToNode(p map[string]any) (model.Node, bool) {
	server := yamlStr(p, "server")
	port := yamlInt(p, "port")
	if port == 0 {
		port = firstPort(yamlStr(p, "ports")) // Hysteria2 port-hopping range
	}
	if server == "" || port <= 0 || port > 65535 {
		return model.Node{}, false
	}

	switch strings.ToLower(yamlStr(p, "type")) {
	case "ss", "shadowsocks":
		return clashShadowsocks(p, server, port)
	case "vmess":
		return clashVMess(p, server, port)
	case "vless":
		return clashVLESS(p, server, port)
	case "trojan":
		return clashTrojan(p, server, port)
	case "hysteria2", "hy2":
		return clashHysteria2(p, server, port)
	case "wireguard", "wg":
		return clashWireGuard(p, server, port)
	default:
		// tuic, snell, ssr, http, socks5, … and anything else are not modeled.
		return model.Node{}, false
	}
}

// clashShadowsocks maps a Clash "ss" proxy. A plugin (obfs, v2ray-plugin, …)
// changes the wire protocol and the bundled sing-box shadowsocks outbound
// carries none, so a plugged node would connect but never handshake — it is
// skipped rather than emitted as a broken outbound.
func clashShadowsocks(p map[string]any, server string, port int) (model.Node, bool) {
	if yamlStr(p, "plugin") != "" {
		return model.Node{}, false
	}
	method := yamlStr(p, "cipher")
	password := yamlStr(p, "password")
	if method == "" || password == "" {
		return model.Node{}, false
	}
	n := model.Node{
		Protocol: model.Shadowsocks,
		Name:     yamlStr(p, "name"),
		Server:   server,
		Port:     port,
		Method:   method,
		Password: password,
	}
	addClashExtra(&n, p, "cipher", "password")
	return n, true
}

// clashVMess maps a Clash "vmess" proxy: uuid, alterId, cipher (→ security), the
// stream transport (network + *-opts), and TLS.
func clashVMess(p map[string]any, server string, port int) (model.Node, bool) {
	uuid := yamlStr(p, "uuid")
	if uuid == "" {
		return model.Node{}, false
	}
	n := model.Node{
		Protocol: model.VMess,
		Name:     yamlStr(p, "name"),
		Server:   server,
		Port:     port,
		UUID:     uuid,
		AlterID:  yamlIntAny(p, "alterId", "alterid", "alter-id"),
		Security: yamlStr(p, "cipher"),
	}
	n.Transport = clashTransport(p)
	n.TLS = clashTLS(p, false)
	addClashExtra(&n, p, "uuid", "alterId", "alterid", "alter-id", "cipher",
		"network", "tls", "servername", "sni", "server-name", "skip-cert-verify",
		"alpn", "client-fingerprint", "fingerprint")
	return n, true
}

// clashVLESS maps a Clash "vless" proxy: uuid, flow, the stream transport, and
// TLS/REALITY (reality-opts → TLS.Reality, client-fingerprint → TLS.Fingerprint).
func clashVLESS(p map[string]any, server string, port int) (model.Node, bool) {
	uuid := yamlStr(p, "uuid")
	if uuid == "" {
		return model.Node{}, false
	}
	n := model.Node{
		Protocol: model.VLESS,
		Name:     yamlStr(p, "name"),
		Server:   server,
		Port:     port,
		UUID:     uuid,
		Flow:     yamlStr(p, "flow"),
	}
	n.Transport = clashTransport(p)
	n.TLS = clashTLS(p, false)
	addClashExtra(&n, p, "uuid", "flow", "network", "tls", "servername", "sni",
		"server-name", "skip-cert-verify", "alpn", "client-fingerprint", "fingerprint")
	return n, true
}

// clashTrojan maps a Clash "trojan" proxy. Trojan is TLS-only, so a TLS block is
// always produced (sni → ServerName).
func clashTrojan(p map[string]any, server string, port int) (model.Node, bool) {
	password := yamlStr(p, "password")
	if password == "" {
		return model.Node{}, false
	}
	n := model.Node{
		Protocol: model.Trojan,
		Name:     yamlStr(p, "name"),
		Server:   server,
		Port:     port,
		Password: password,
	}
	n.Transport = clashTransport(p)
	n.TLS = clashTLS(p, true)
	addClashExtra(&n, p, "password", "network", "sni", "servername", "server-name",
		"skip-cert-verify", "alpn", "client-fingerprint", "fingerprint")
	return n, true
}

// clashHysteria2 maps a Clash "hysteria2" proxy. Hysteria2 is always over TLS and
// carries Salamander obfuscation via obfs/obfs-password.
func clashHysteria2(p map[string]any, server string, port int) (model.Node, bool) {
	password := yamlStrAny(p, "password", "auth-str", "auth")
	if password == "" {
		return model.Node{}, false
	}
	n := model.Node{
		Protocol: model.Hysteria2,
		Name:     yamlStr(p, "name"),
		Server:   server,
		Port:     port,
		Password: password,
	}
	n.TLS = clashTLS(p, true)
	if strings.EqualFold(yamlStr(p, "obfs"), "salamander") {
		n.Obfs = &model.Obfs{Type: "salamander", Password: yamlStr(p, "obfs-password")}
	}
	addClashExtra(&n, p, "password", "auth-str", "auth", "sni", "servername",
		"server-name", "skip-cert-verify", "alpn", "obfs", "obfs-password")
	return n, true
}

// clashWireGuard maps a Clash "wireguard" proxy onto an AmneziaWG node (the
// model's single WG-family protocol). The AmneziaWG obfuscation knobs are read
// when present even though the bundled sing-box degrades them to plain
// WireGuard, so the parsed node stays faithful to the source.
func clashWireGuard(p map[string]any, server string, port int) (model.Node, bool) {
	priv := yamlStrAny(p, "private-key", "private_key")
	pub := yamlStrAny(p, "public-key", "public_key")
	if priv == "" || pub == "" {
		return model.Node{}, false
	}
	wg := &model.WireGuard{
		PrivateKey:    priv,
		PeerPublicKey: pub,
		PreSharedKey:  yamlStrAny(p, "pre-shared-key", "preshared-key", "pre_shared_key"),
		LocalAddress:  clashWGAddresses(p),
	}
	applyClashAmnezia(wg, p)
	n := model.Node{
		Protocol:  model.AmneziaWG,
		Name:      yamlStr(p, "name"),
		Server:    server,
		Port:      port,
		WireGuard: wg,
	}
	addClashExtra(&n, p, "private-key", "private_key", "public-key", "public_key",
		"pre-shared-key", "preshared-key", "pre_shared_key", "ip", "ipv6", "addresses",
		"jc", "jmin", "jmax", "s1", "s2", "h1", "h2", "h3", "h4", "amnezia-wg-option")
	return n, true
}

// clashTransport reads the Clash "network" field and its matching *-opts block
// into a model.Transport, returning nil for plain TCP (the common case) so the
// node omits the transport.
func clashTransport(p map[string]any) *model.Transport {
	switch strings.ToLower(yamlStr(p, "network")) {
	case "", "tcp", "none", "raw", "http/1.1":
		return nil
	case "ws", "websocket":
		t := &model.Transport{Type: "ws"}
		if o := yamlMap(p, "ws-opts"); o != nil {
			t.Path = yamlStr(o, "path")
			t.Host = headerHost(yamlMap(o, "headers"))
		}
		return t
	case "grpc":
		t := &model.Transport{Type: "grpc"}
		if o := yamlMap(p, "grpc-opts"); o != nil {
			t.ServiceName = yamlStrAny(o, "grpc-service-name", "serviceName", "service-name")
		}
		return t
	case "h2":
		t := &model.Transport{Type: "http"}
		if o := yamlMap(p, "h2-opts"); o != nil {
			t.Path = yamlStr(o, "path")
			t.Host = first(yamlStrings(o, "host"))
		}
		return t
	case "http":
		t := &model.Transport{Type: "http"}
		if o := yamlMap(p, "http-opts"); o != nil {
			t.Path = first(yamlStrings(o, "path"))
			t.Host = headerHost(yamlMap(o, "headers"))
		}
		return t
	case "httpupgrade":
		t := &model.Transport{Type: "httpupgrade"}
		if o := yamlMap(p, "ws-opts"); o != nil { // mihomo reuses ws-opts for httpupgrade
			t.Path = yamlStr(o, "path")
			t.Host = headerHost(yamlMap(o, "headers"))
		}
		return t
	default:
		return nil
	}
}

// clashTLS builds a model.TLS from the proxy's tls / reality / sni fields.
// forceTLS is set for protocols that are always over TLS (trojan, hysteria2), so
// a config that omits "tls: true" still gets a TLS block. REALITY implies TLS.
// Returns nil when TLS is neither requested nor forced.
func clashTLS(p map[string]any, forceTLS bool) *model.TLS {
	reality := yamlMap(p, "reality-opts")
	if !(forceTLS || yamlBool(p, "tls") || reality != nil) {
		return nil
	}
	t := &model.TLS{
		Enabled:     true,
		ServerName:  yamlStrAny(p, "servername", "sni", "server-name"),
		Insecure:    yamlBool(p, "skip-cert-verify"),
		ALPN:        yamlStrings(p, "alpn"),
		Fingerprint: yamlStrAny(p, "client-fingerprint", "fingerprint"),
	}
	if reality != nil {
		t.Reality = &model.Reality{
			PublicKey: yamlStrAny(reality, "public-key", "public_key"),
			ShortID:   yamlStrAny(reality, "short-id", "short_id"),
		}
	}
	return t
}

// clashWGAddresses gathers the tunnel-local addresses from a WireGuard proxy,
// normalizing a bare IP to a /32 (v4) or /128 (v6) so sing-box accepts it.
func clashWGAddresses(p map[string]any) []string {
	var addrs []string
	addrs = append(addrs, yamlStrings(p, "addresses")...)
	if s := yamlStr(p, "ip"); s != "" {
		addrs = append(addrs, s)
	}
	if s := yamlStr(p, "ipv6"); s != "" {
		addrs = append(addrs, s)
	}
	for i, a := range addrs {
		if !strings.Contains(a, "/") {
			if strings.Contains(a, ":") {
				addrs[i] = a + "/128"
			} else {
				addrs[i] = a + "/32"
			}
		}
	}
	return addrs
}

// applyClashAmnezia copies the AmneziaWG obfuscation knobs, read either at the
// proxy's top level or from an amnezia-wg-option sub-map, into the WireGuard
// config. All-zero means plain WireGuard.
func applyClashAmnezia(wg *model.WireGuard, p map[string]any) {
	src := p
	if o := yamlMap(p, "amnezia-wg-option"); o != nil {
		src = o
	}
	wg.Jc = yamlInt(src, "jc")
	wg.Jmin = yamlInt(src, "jmin")
	wg.Jmax = yamlInt(src, "jmax")
	wg.S1 = yamlInt(src, "s1")
	wg.S2 = yamlInt(src, "s2")
	wg.H1 = yamlInt64(src, "h1")
	wg.H2 = yamlInt64(src, "h2")
	wg.H3 = yamlInt64(src, "h3")
	wg.H4 = yamlInt64(src, "h4")
}

// addClashExtra preserves scalar fields the mapper did not model explicitly into
// Node.Extra, mirroring how the link parsers keep unknown query params so a
// round-trip never silently drops information. Nested blocks (maps/sequences) are
// not flattened.
func addClashExtra(n *model.Node, p map[string]any, consumed ...string) {
	skip := map[string]bool{"name": true, "type": true, "server": true, "port": true}
	for _, k := range consumed {
		skip[k] = true
	}
	for k, v := range p {
		if skip[k] {
			continue
		}
		s, ok := v.(string)
		if !ok || s == "" {
			continue
		}
		if n.Extra == nil {
			n.Extra = map[string]string{}
		}
		n.Extra[k] = s
	}
}

// headerHost extracts the Host header from a ws/http headers map, tolerating both
// a scalar and a single-element list value.
func headerHost(h map[string]any) string {
	if h == nil {
		return ""
	}
	if s := yamlStrAny(h, "Host", "host"); s != "" {
		return s
	}
	if ss := yamlStrings(h, "Host"); len(ss) > 0 {
		return ss[0]
	}
	return first(yamlStrings(h, "host"))
}

// yamlStr returns m[key] when it is a scalar string, else "".
func yamlStr(m map[string]any, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// yamlStrAny returns the first non-empty scalar among the given keys.
func yamlStrAny(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s := yamlStr(m, k); s != "" {
			return s
		}
	}
	return ""
}

// yamlInt parses m[key] as an int, returning 0 when absent or non-numeric.
func yamlInt(m map[string]any, key string) int {
	if s := strings.TrimSpace(yamlStr(m, key)); s != "" {
		if i, err := strconv.Atoi(s); err == nil {
			return i
		}
	}
	return 0
}

// yamlIntAny parses the first present, numeric value among the given keys.
func yamlIntAny(m map[string]any, keys ...string) int {
	for _, k := range keys {
		if _, ok := m[k]; ok {
			return yamlInt(m, k)
		}
	}
	return 0
}

// yamlInt64 parses m[key] as an int64 (the H1-H4 AmneziaWG knobs exceed int32).
func yamlInt64(m map[string]any, key string) int64 {
	if s := strings.TrimSpace(yamlStr(m, key)); s != "" {
		if i, err := strconv.ParseInt(s, 10, 64); err == nil {
			return i
		}
	}
	return 0
}

// yamlBool reports whether m[key] is a truthy scalar (true/1/yes/on).
func yamlBool(m map[string]any, key string) bool {
	return isTruthy(yamlStr(m, key))
}

// yamlMap returns m[key] when it is a nested mapping, else nil.
func yamlMap(m map[string]any, key string) map[string]any {
	if v, ok := m[key]; ok {
		if mm, ok := v.(map[string]any); ok {
			return mm
		}
	}
	return nil
}

// yamlStrings returns m[key] as a string slice, accepting either a sequence of
// scalars or a single scalar (which becomes a one-element slice). Empty entries
// are dropped.
func yamlStrings(m map[string]any, key string) []string {
	v, ok := m[key]
	if !ok {
		return nil
	}
	switch x := v.(type) {
	case []any:
		var out []string
		for _, e := range x {
			if s, ok := e.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	case string:
		if x != "" {
			return []string{x}
		}
	}
	return nil
}

// first returns the first element of a slice, or "" when it is empty.
func first(ss []string) string {
	if len(ss) > 0 {
		return ss[0]
	}
	return ""
}

// firstPort parses the first port number out of a Clash "ports" range string
// (e.g. "443,8443-8600"), returning 0 when none is valid.
func firstPort(s string) int {
	start := -1
	for i := 0; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			start = i
			break
		}
	}
	if start < 0 {
		return 0
	}
	end := start
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	p, err := strconv.Atoi(s[start:end])
	if err != nil || p <= 0 || p > 65535 {
		return 0
	}
	return p
}
