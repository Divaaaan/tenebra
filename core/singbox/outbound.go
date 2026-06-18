package singbox

import (
	"fmt"
	"strings"

	"github.com/Divaaaan/tenebra/core/model"
)

// outbound converts one node into its sing-box outbound object. The returned
// tag is what the selector and route rules reference. WireGuard/AmneziaWG is
// handled separately (see endpoint), because newer sing-box models it as a
// top-level endpoint rather than an outbound; this returns ok=false for it so
// the builder routes it through that path.
func outbound(n model.Node, tag string) (obj map[string]any, ok bool, err error) {
	switch n.Protocol {
	case model.VLESS:
		return vlessOutbound(n, tag), true, nil
	case model.Hysteria2:
		return hysteria2Outbound(n, tag), true, nil
	case model.Shadowsocks:
		return shadowsocksOutbound(n, tag), true, nil
	case model.Trojan:
		return trojanOutbound(n, tag), true, nil
	case model.VMess:
		return vmessOutbound(n, tag), true, nil
	case model.AmneziaWG:
		return nil, false, nil // built as an endpoint, not an outbound
	case "":
		return nil, false, fmt.Errorf("node %q: empty protocol", n.Name)
	default:
		return nil, false, fmt.Errorf("node %q: unsupported protocol %q", n.Name, n.Protocol)
	}
}

// vlessOutbound builds the sing-box vless outbound, attaching flow, TLS/REALITY,
// and the stream transport only when the node carries them.
func vlessOutbound(n model.Node, tag string) map[string]any {
	o := map[string]any{
		"type":        "vless",
		"tag":         tag,
		"server":      n.Server,
		"server_port": n.Port,
		"uuid":        n.UUID,
	}
	if n.Flow != "" {
		o["flow"] = n.Flow
	}
	if t := tlsObject(n.TLS); t != nil {
		o["tls"] = t
	}
	if tr := transportObject(n.Transport); tr != nil {
		o["transport"] = tr
	}
	return o
}

// hysteria2Outbound builds the sing-box hysteria2 outbound. Hysteria2 is always
// over TLS, so a TLS object is emitted even when the node lacks one.
func hysteria2Outbound(n model.Node, tag string) map[string]any {
	o := map[string]any{
		"type":        "hysteria2",
		"tag":         tag,
		"server":      n.Server,
		"server_port": n.Port,
		"password":    n.Password,
	}
	if n.Obfs != nil && n.Obfs.Type != "" {
		o["obfs"] = map[string]any{
			"type":     n.Obfs.Type,
			"password": n.Obfs.Password,
		}
	}
	// Hysteria2 always runs over TLS; emit a TLS object even if the node didn't
	// carry one so the outbound is valid, defaulting SNI to the server.
	if t := tlsObject(n.TLS); t != nil {
		o["tls"] = t
	} else {
		o["tls"] = map[string]any{"enabled": true, "server_name": n.Server}
	}
	return o
}

// shadowsocksOutbound builds the sing-box shadowsocks outbound. The cipher and
// password come straight from the node; shadowsocks carries no TLS of its own.
func shadowsocksOutbound(n model.Node, tag string) map[string]any {
	return map[string]any{
		"type":        "shadowsocks",
		"tag":         tag,
		"server":      n.Server,
		"server_port": n.Port,
		"method":      n.Method,
		"password":    n.Password,
	}
}

// trojanOutbound builds the sing-box trojan outbound. Trojan is TLS-only, so a
// TLS object is emitted even when the node lacks one, plus any stream transport.
func trojanOutbound(n model.Node, tag string) map[string]any {
	o := map[string]any{
		"type":        "trojan",
		"tag":         tag,
		"server":      n.Server,
		"server_port": n.Port,
		"password":    n.Password,
	}
	if t := tlsObject(n.TLS); t != nil {
		o["tls"] = t
	} else {
		// Trojan is TLS-only by design.
		o["tls"] = map[string]any{"enabled": true, "server_name": n.Server}
	}
	if tr := transportObject(n.Transport); tr != nil {
		o["transport"] = tr
	}
	return o
}

// vmessOutbound builds the sing-box vmess outbound, defaulting the cipher to
// "auto" and attaching TLS and the stream transport when the node carries them.
func vmessOutbound(n model.Node, tag string) map[string]any {
	o := map[string]any{
		"type":        "vmess",
		"tag":         tag,
		"server":      n.Server,
		"server_port": n.Port,
		"uuid":        n.UUID,
		"alter_id":    n.AlterID,
	}
	// security defaults to "auto" when unspecified, matching common VMess links.
	if n.Security != "" {
		o["security"] = n.Security
	} else {
		o["security"] = "auto"
	}
	if t := tlsObject(n.TLS); t != nil {
		o["tls"] = t
	}
	if tr := transportObject(n.Transport); tr != nil {
		o["transport"] = tr
	}
	return o
}

// tlsObject renders a model.TLS into the sing-box tls sub-object, or nil when
// TLS is absent/disabled. REALITY and uTLS are only added when present so we
// never emit empty enabled:false sub-objects.
func tlsObject(t *model.TLS) map[string]any {
	if t == nil || !t.Enabled {
		return nil
	}
	o := map[string]any{"enabled": true}
	if t.ServerName != "" {
		o["server_name"] = t.ServerName
	}
	if t.Insecure {
		o["insecure"] = true
	}
	if len(t.ALPN) > 0 {
		o["alpn"] = t.ALPN
	}
	// A REALITY block is only valid with a non-empty public_key: sing-box FATALs
	// with "invalid public_key" otherwise, and a keyless reality outbound could
	// never complete a handshake anyway. The builder already skips such nodes
	// (see buildNodes), but guard here too so tlsObject never emits a reality
	// block with an empty public_key even if reached by another path.
	reality := t.Reality != nil && t.Reality.PublicKey != ""
	// REALITY requires a uTLS fingerprint: sing-box FATALs with "uTLS is required
	// by reality client" if one is missing. Real subscriptions usually send
	// fp=chrome, but a node that omits it must not sink the whole config, so
	// default the fingerprint to chrome whenever REALITY is in use.
	fingerprint := t.Fingerprint
	if fingerprint == "" && reality {
		fingerprint = "chrome"
	}
	if fingerprint != "" {
		o["utls"] = map[string]any{
			"enabled":     true,
			"fingerprint": fingerprint,
		}
	}
	if reality {
		r := map[string]any{
			"enabled":    true,
			"public_key": t.Reality.PublicKey,
		}
		if t.Reality.ShortID != "" {
			r["short_id"] = t.Reality.ShortID
		}
		o["reality"] = r
	}
	return o
}

// transportObject renders a model.Transport into the sing-box transport
// sub-object, or nil for raw TCP (empty type). Only ws and grpc are commonly
// used; http/httpupgrade pass through with their path.
func transportObject(t *model.Transport) map[string]any {
	if t == nil || t.Type == "" {
		return nil
	}
	switch t.Type {
	case "ws":
		o := map[string]any{"type": "ws"}
		if t.Path != "" {
			o["path"] = t.Path
		}
		if t.Host != "" {
			o["headers"] = map[string]any{"Host": t.Host}
		}
		return o
	case "grpc":
		o := map[string]any{"type": "grpc"}
		if t.ServiceName != "" {
			o["service_name"] = t.ServiceName
		}
		return o
	case "http", "httpupgrade":
		o := map[string]any{"type": t.Type}
		if t.Path != "" {
			o["path"] = t.Path
		}
		if t.Host != "" {
			// http transport takes a host list; httpupgrade takes a single host.
			if t.Type == "http" {
				o["host"] = []string{t.Host}
			} else {
				o["host"] = t.Host
			}
		}
		return o
	default:
		// Unknown transport: pass the type through verbatim plus any path so the
		// information isn't lost; sing-box will reject it loudly if invalid.
		o := map[string]any{"type": t.Type}
		if t.Path != "" {
			o["path"] = t.Path
		}
		return o
	}
}

// sanitizeTag derives a stable, readable outbound tag from a node name. sing-box
// tags must be unique and are referenced by the selector and routes; the builder
// dedupes collisions by suffixing. Control characters and quotes are stripped to
// keep the JSON clean.
func sanitizeTag(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "node"
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r < 0x20 || r == 0x7f: // control chars
			b.WriteByte('-')
		case r == '"' || r == '\\':
			b.WriteByte('-')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return "node"
	}
	return out
}
