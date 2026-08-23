package control

import "regexp"

// Secret masking for free-form diagnostic text.
//
// The struct-level redaction elsewhere in this package (redactProfile) keeps
// credentials out of the protocol's typed replies. This is the other half: a
// diagnostics bundle is assembled from log lines and error strings, where a
// subscription token or a node UUID can turn up in text nobody designed. The
// bundle exists to be pasted into a chat with a stranger, so it is scrubbed on
// the way out.
//
// The patterns mirror ui-desktop/src/lib/diagnostics.ts one for one, so the
// bundle the core produces and the one the app copies from its log console mask
// the same things. Deliberately light: it targets the few shapes that actually
// carry a secret and leaves protocols, hosts, ports and error text readable,
// because those are the entire point of a diagnostics dump.
var (
	// managedToken matches a link to a managed subscription host, whose last path
	// segment is the per-user token. The host and the path shape are useful
	// context and stay; only the token is dropped.
	managedToken = regexp.MustCompile(`(?i)(https?://\S*(?:vpsxd\.pro|vpnxd\.pro|chatakfix)\S*/)([^\s/?#]+)`)
	// shareLinkUserinfo matches the node credential a share link carries as the
	// userinfo before the @ (a UUID or a password). The scheme and host say what
	// and where, and survive.
	shareLinkUserinfo = regexp.MustCompile(`(?i)\b(vless|vmess|trojan|ss|ssr|hysteria2?|hy2|tuic|socks5?)://[^\s/@]+@`)
	// bareUUID matches a UUID anywhere. In this app's logs one is almost always a
	// node credential (a VLESS/VMess id), so it is masked wherever it appears —
	// including in a message that only meant to name a node.
	bareUUID = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
)

// scrubSecrets masks subscription tokens and node credentials in a block of
// text, leaving everything else readable.
func scrubSecrets(text string) string {
	text = managedToken.ReplaceAllString(text, "$1***")
	text = shareLinkUserinfo.ReplaceAllString(text, "$1://***@")
	text = bareUUID.ReplaceAllString(text, "***")
	return text
}
