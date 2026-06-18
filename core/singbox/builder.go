// Package singbox builds a complete sing-box configuration (as a
// map[string]any ready for encoding/json) from a set of normalized nodes plus
// routing options. It targets the sing-box 1.11+ schema and deliberately does
// not import sing-box: the config is plain JSON so the core stays dependency-free
// and unit-testable offline.
package singbox

import (
	"fmt"

	"github.com/Divaaaan/tenebra/core/model"
	"github.com/Divaaaan/tenebra/core/routing"
)

// Default tun and clash-api settings. The address is a small private /30 unused
// by common LANs; auto_route/strict_route are forced on by Build.
const (
	defaultInterfaceName = "tenebra"
	defaultMTU           = 9000
	defaultStack         = "system"
	defaultClashAPIPort  = 9090

	tunTag    = "tun-in"
	tunAddr   = "172.19.0.1/30"
	proxyTag  = "proxy"
	directTag = "direct"
	blockTag  = "block"
)

// TunOptions configures the single tun inbound and the clash API. The zero
// value is filled with defaults by Build.
type TunOptions struct {
	InterfaceName string
	MTU           int
	Stack         string // system, gvisor, or mixed
	ClashAPIPort  int
}

func (t TunOptions) normalize() TunOptions {
	if t.InterfaceName == "" {
		t.InterfaceName = defaultInterfaceName
	}
	if t.MTU == 0 {
		t.MTU = defaultMTU
	}
	if t.Stack == "" {
		t.Stack = defaultStack
	}
	if t.ClashAPIPort == 0 {
		t.ClashAPIPort = defaultClashAPIPort
	}
	return t
}

// Build assembles the full sing-box config. selectedTag is the node tag the
// proxy selector defaults to; if empty (or not among the nodes) the first
// usable node is used. Nodes with unknown or zero protocols are skipped and
// reported via the returned error only when no usable node remains; otherwise
// they are silently dropped so one bad entry doesn't sink a whole subscription.
//
// It returns an error when there are no usable nodes or when routing options
// are invalid.
func Build(nodes []model.Node, selectedTag string, ro routing.Options, tun TunOptions) (map[string]any, error) {
	ro = ro.Normalize()
	if err := ro.Validate(); err != nil {
		return nil, err
	}
	tun = tun.normalize()

	outs, endpoints, tags, err := buildNodes(nodes)
	if err != nil {
		return nil, err
	}
	if len(tags) == 0 {
		return nil, fmt.Errorf("singbox: no usable nodes")
	}

	// Resolve the selector default: honor selectedTag when present, else first.
	def := selectedTag
	if def == "" || !contains(tags, def) {
		def = tags[0]
	}

	// Shared outbounds: the selector over all nodes, plus direct/block.
	selector := map[string]any{
		"type":      "selector",
		"tag":       proxyTag,
		"outbounds": tags,
		"default":   def,
	}
	outbounds := make([]map[string]any, 0, len(outs)+3)
	outbounds = append(outbounds, selector)
	outbounds = append(outbounds, outs...)
	outbounds = append(outbounds,
		map[string]any{"type": "direct", "tag": directTag},
		map[string]any{"type": "block", "tag": blockTag},
	)

	cfg := map[string]any{
		"log": map[string]any{
			// warn, not info: at info sing-box logs (and formats) every connection
			// and DNS exchange, which is real per-packet work during the burst of
			// reconnections right after the tunnel comes up — exactly when the UI
			// feels least responsive. warn keeps the genuinely useful lines.
			"level":     "warn",
			"timestamp": true,
		},
		"dns":       ro.DNS(),
		"inbounds":  []map[string]any{tunInbound(tun)},
		"outbounds": outbounds,
		"route":     routeBlock(ro),
		"experimental": map[string]any{
			"clash_api": map[string]any{
				"external_controller": fmt.Sprintf("127.0.0.1:%d", tun.ClashAPIPort),
			},
			"cache_file": map[string]any{
				"enabled": true,
			},
		},
	}

	// Endpoints (WireGuard/AmneziaWG) live in their own top-level array in
	// sing-box 1.11+. Only emit the key when there's something in it.
	if len(endpoints) > 0 {
		cfg["endpoints"] = endpoints
	}

	return cfg, nil
}

// buildNodes converts nodes into outbounds and endpoints, assigning each a
// unique tag. It returns the outbound objects, endpoint objects, and the
// ordered list of node tags (outbounds then endpoints) for the selector. A node
// that fails to convert because of missing required fields aborts the build;
// unknown/zero protocols are skipped.
func buildNodes(nodes []model.Node) (outs, endpoints []map[string]any, tags []string, err error) {
	seen := map[string]int{}
	uniq := func(name string) string {
		base := sanitizeTag(name)
		if seen[base] == 0 {
			seen[base] = 1
			return base
		}
		// Collision: suffix with an incrementing counter until free.
		for {
			seen[base]++
			cand := fmt.Sprintf("%s-%d", base, seen[base])
			if seen[cand] == 0 {
				seen[cand] = 1
				return cand
			}
		}
	}

	for _, n := range nodes {
		if n.Protocol == "" {
			continue // skip zero-protocol entries
		}
		tag := uniq(n.Name)

		if n.Protocol == model.AmneziaWG {
			ep, epErr := wireguardEndpoint(n, tag)
			if epErr != nil {
				return nil, nil, nil, epErr
			}
			endpoints = append(endpoints, ep)
			tags = append(tags, tag)
			continue
		}

		obj, ok, oErr := outbound(n, tag)
		if oErr != nil {
			// Unknown protocol: skip rather than abort, freeing the tag.
			delete(seen, tag)
			continue
		}
		if !ok {
			continue
		}
		outs = append(outs, obj)
		tags = append(tags, tag)
	}
	return outs, endpoints, tags, nil
}

// tunInbound renders the single tun inbound. strict_route is forced on: it is
// what makes the kill switch real — with auto_route + strict_route, sing-box
// installs routes that prevent traffic from escaping the tunnel, so when the
// proxy is down packets are dropped rather than leaking to the physical
// interface. Combined with route final=proxy (no implicit direct), there is no
// path for proxied traffic to bypass the tunnel.
func tunInbound(t TunOptions) map[string]any {
	return map[string]any{
		"type":           "tun",
		"tag":            tunTag,
		"interface_name": t.InterfaceName,
		"address":        []string{tunAddr},
		"auto_route":     true,
		"strict_route":   true,
		"mtu":            t.MTU,
		"stack":          t.Stack,
	}
}

// routeBlock renders the route section from routing options plus the standard
// auto_detect_interface and final outbound.
func routeBlock(ro routing.Options) map[string]any {
	r := map[string]any{
		"rules":                   ro.RouteRules(),
		"final":                   ro.FinalOutbound(),
		"auto_detect_interface":   true,
		"default_domain_resolver": ro.DefaultDomainResolver(),
	}
	if rs := ro.RouteRuleSets(); len(rs) > 0 {
		r["rule_set"] = rs
	}
	return r
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
