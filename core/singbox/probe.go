package singbox

import (
	"fmt"

	"github.com/Divaaaan/tenebra/core/model"
)

// ProbeBinding ties one probe listener to the node it exercises. The caller
// drives a request at 127.0.0.1:Port and attributes the outcome to Tag.
type ProbeBinding struct {
	// Tag is the outbound tag the builder minted for this node, i.e. the same
	// identity Build/SelectorTags use.
	Tag string
	// Name is the node's display name, for surfacing results in the UI.
	Name string
	// Port is the loopback port whose traffic is pinned to this node.
	Port int
}

// probeInboundTag names the listener pinned to the binding at index i. Tags must
// be unique per inbound because the route rules match on them.
func probeInboundTag(i int) string { return fmt.Sprintf("probe-in-%d", i) }

// BuildProbe renders a config that exposes every node as its own loopback proxy,
// so a caller can measure what actually survives each node instead of whether
// its address accepts TCP.
//
// One process, N listeners — not N processes: sing-box start-up dominates a
// probe run, and a single instance also keeps one shared DNS cache, so nodes are
// compared under the same conditions rather than each paying its own cold
// resolve.
//
// Two properties matter for the result to mean anything:
//
//   - No tun inbound and no auto_route. The probe must never touch the system
//     routing table: it runs while a tunnel (possibly someone else's) is up, and
//     a second tun with its own default route is exactly the failure that takes
//     a machine's connectivity down.
//   - route.final is block, not direct. If a probe request somehow misses its
//     rule, blocking makes it fail; letting it egress directly would return a
//     healthy verdict measured over the *unproxied* link — the probe would
//     certify a dead node as working, which is the bug this whole path exists to
//     prevent.
//
// Ports are assigned basePort, basePort+1, … over the nodes that survive
// validation, in builder order. Nodes the builder skips (unknown protocol,
// missing credentials, keyless REALITY) get no binding — they cannot be dialed,
// so there is nothing to measure. Returns an error when no node is usable.
func BuildProbe(nodes []model.Node, basePort int) (map[string]any, []ProbeBinding, error) {
	if basePort < 1 || basePort > 65535 {
		return nil, nil, fmt.Errorf("singbox: probe base port %d out of range", basePort)
	}

	outs, endpoints, sel, err := buildNodes(nodes)
	if err != nil {
		return nil, nil, err
	}
	if len(sel) == 0 {
		return nil, nil, fmt.Errorf("singbox: no usable nodes")
	}
	if basePort+len(sel)-1 > 65535 {
		return nil, nil, fmt.Errorf("singbox: probe needs %d ports from %d, past 65535", len(sel), basePort)
	}

	bindings := make([]ProbeBinding, 0, len(sel))
	inbounds := make([]map[string]any, 0, len(sel))
	rules := make([]map[string]any, 0, len(sel)+1)

	for i, nt := range sel {
		port := basePort + i
		inTag := probeInboundTag(i)
		// NodeTag carries the input index, not the name; resolve it back so the
		// UI can label a result without re-deriving the builder's tag scheme.
		name := ""
		if nt.Index >= 0 && nt.Index < len(nodes) {
			name = nodes[nt.Index].Name
		}

		inbounds = append(inbounds, map[string]any{
			"type": "mixed",
			"tag":  inTag,
			// Loopback only: a probe listener reachable off the machine would let
			// any host on the LAN relay through the user's nodes.
			"listen":      mixedListen,
			"listen_port": port,
		})
		rules = append(rules, map[string]any{
			"inbound":  []string{inTag},
			"action":   "route",
			"outbound": nt.Tag,
		})
		bindings = append(bindings, ProbeBinding{Tag: nt.Tag, Name: name, Port: port})
	}

	outbounds := make([]map[string]any, 0, len(outs)+1)
	outbounds = append(outbounds, outs...)
	// block exists so route.final can deny rather than leak; direct is
	// deliberately absent so no rule can accidentally name it.
	outbounds = append(outbounds, map[string]any{"type": "block", "tag": blockTag})

	cfg := map[string]any{
		"log": map[string]any{"level": "warn", "timestamp": true},
		// Resolution goes through the node being probed, so a node that answers
		// TCP but cannot carry DNS is scored as the failure it is. sniff runs
		// first so rules see the requested domain.
		"inbounds":  inbounds,
		"outbounds": outbounds,
		"route": map[string]any{
			"rules": append([]map[string]any{{"action": "sniff"}}, rules...),
			"final": blockTag,
		},
	}
	if len(endpoints) > 0 {
		cfg["endpoints"] = endpoints
	}

	return cfg, bindings, nil
}
