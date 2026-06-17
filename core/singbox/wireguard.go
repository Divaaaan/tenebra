package singbox

import (
	"fmt"

	"github.com/Divaaaan/tenebra/core/model"
)

// wireguardEndpoint builds a sing-box WireGuard endpoint from a node.
//
// sing-box 1.11 moved WireGuard out of "outbounds" into a top-level
// "endpoints" array (type "wireguard"); the old wireguard *outbound* is
// deprecated and removed in 1.12. We emit the endpoint form: an "address"
// list for the local tunnel addresses and a "peers" array carrying the remote
// public key, endpoint host/port, allowed_ips and optional pre_shared_key.
//
// AmneziaWG note: upstream SagerNet/sing-box does NOT support AmneziaWG; the
// AmneziaWG 2.0 request was closed as not-planned, and none of the
// Jc/Jmin/Jmax/S1/S2/H1-H4 knobs exist in its WireGuard schema. They are
// emitted here for AmneziaWG-capable forks (e.g. amnezia-box,
// sing-box-extended), where these params live on the endpoint. Stock sing-box
// will reject the unknown keys, so a caller targeting stock sing-box must strip
// them. Field names follow the common fork convention but are NOT guaranteed to
// match a given fork's option package — verify before relying on them. We do
// not fake upstream support. The knobs are only attached when at least one set.
func wireguardEndpoint(n model.Node, tag string) (map[string]any, error) {
	wg := n.WireGuard
	if wg == nil {
		return nil, fmt.Errorf("node %q: amneziawg without wireguard config", n.Name)
	}
	if wg.PrivateKey == "" {
		return nil, fmt.Errorf("node %q: wireguard missing private_key", n.Name)
	}
	if wg.PeerPublicKey == "" {
		return nil, fmt.Errorf("node %q: wireguard missing peer_public_key", n.Name)
	}

	peer := map[string]any{
		"address":    n.Server,
		"port":       n.Port,
		"public_key": wg.PeerPublicKey,
		// Route all traffic into the tunnel; the sing-box route block decides
		// what actually reaches this endpoint.
		"allowed_ips": []string{"0.0.0.0/0", "::/0"},
	}
	if wg.PreSharedKey != "" {
		peer["pre_shared_key"] = wg.PreSharedKey
	}

	ep := map[string]any{
		"type":        "wireguard",
		"tag":         tag,
		"address":     localAddresses(wg.LocalAddress),
		"private_key": wg.PrivateKey,
		"peers":       []map[string]any{peer},
	}

	// AmneziaWG obfuscation — fork-only, see doc comment above.
	if amneziaSet(wg) {
		applyAmnezia(ep, wg)
	}

	return ep, nil
}

// localAddresses returns the tunnel-local addresses, defaulting to a benign
// private /32 when the node didn't carry one so the endpoint stays valid.
func localAddresses(addrs []string) []string {
	if len(addrs) == 0 {
		return []string{"172.16.0.2/32"}
	}
	return addrs
}

// amneziaSet reports whether any AmneziaWG obfuscation knob is non-zero.
func amneziaSet(wg *model.WireGuard) bool {
	return wg.Jc != 0 || wg.Jmin != 0 || wg.Jmax != 0 ||
		wg.S1 != 0 || wg.S2 != 0 ||
		wg.H1 != 0 || wg.H2 != 0 || wg.H3 != 0 || wg.H4 != 0
}

// applyAmnezia attaches the AmneziaWG obfuscation parameters. These keys are
// only understood by AmneziaWG-capable sing-box builds.
func applyAmnezia(ep map[string]any, wg *model.WireGuard) {
	set := func(k string, v int) {
		if v != 0 {
			ep[k] = v
		}
	}
	set("jc", wg.Jc)
	set("jmin", wg.Jmin)
	set("jmax", wg.Jmax)
	set("s1", wg.S1)
	set("s2", wg.S2)
	if wg.H1 != 0 {
		ep["h1"] = wg.H1
	}
	if wg.H2 != 0 {
		ep["h2"] = wg.H2
	}
	if wg.H3 != 0 {
		ep["h3"] = wg.H3
	}
	if wg.H4 != 0 {
		ep["h4"] = wg.H4
	}
}
