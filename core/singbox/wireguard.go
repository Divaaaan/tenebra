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
// AmneziaWG note: stock SagerNet/sing-box (the binary Tenebra bundles) does NOT
// support AmneziaWG. Its WireGuard schema has none of the Jc/Jmin/Jmax/S1/S2/
// H1-H4 obfuscation knobs and rejects them at config decode with an "unknown
// field" fatal, which would sink the whole config. We therefore emit a plain
// WireGuard endpoint and never serialize those knobs; an AmneziaWG node degrades
// to plain WireGuard rather than poisoning the config. Real AmneziaWG support
// would require a fork (e.g. amnezia-box) and a build that links it — see
// applyAmnezia, kept as a no-op marker for that future.
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
