// Package singbox builds a complete sing-box configuration (as a
// map[string]any ready for encoding/json) from a set of normalized nodes plus
// routing options. It targets the sing-box 1.11+ schema and deliberately does
// not import sing-box: the config is plain JSON so the core stays dependency-free
// and unit-testable offline.
package singbox

import (
	"crypto/rand"
	"fmt"
	"net"
	"path/filepath"
	"strconv"

	"github.com/Divaaaan/tenebra/core/model"
	"github.com/Divaaaan/tenebra/core/routing"
)

// Default tun and clash-api settings. The tun carries both an IPv4 and an IPv6
// address: a small private /30 unused by common LANs, plus a ULA /126 (fc00::/7
// space, RFC 4193). auto_route claims a default route for every address family
// the tun holds, so without the IPv6 prefix auto_route would only hijack IPv4
// and native IPv6 traffic on a dual-stack host would egress around the tunnel —
// a silent leak. The ULA is inert on a single-stack host (no IPv6 = nothing to
// claim), so it is safe to include unconditionally. auto_route/strict_route are
// forced on by Build for a sing-box-owned tun, and suppressed when the platform
// owns the tun and its routing (TunOptions.ExternalTun, the mobile case).
const (
	defaultMTU          = 9000
	defaultStack        = StackSystem
	defaultClashAPIPort = 9090

	tunTag    = "tun-in"
	tunAddr   = "172.19.0.1/30"
	tunAddr6  = "fdfe:dcba:9876::1/126"
	proxyTag  = "proxy"
	directTag = "direct"
	blockTag  = "block"

	// System-proxy mode replaces the tun inbound with a single mixed inbound
	// (HTTP + SOCKS on one port) bound to loopback; the client then points the OS
	// at that address. mixedListen is loopback-only on purpose — the inbound must
	// never be reachable off the machine, or any host on the LAN could relay
	// through the user's tunnel. The port is configurable (TunOptions.MixedPort)
	// and defaults to DefaultMixedPort, chosen away from the clash API's 9090.
	mixedTag    = "mixed-in"
	mixedListen = "127.0.0.1"
)

// DefaultMixedPort is the loopback port the mixed inbound listens on in
// ModeSystemProxy when none is configured. It is exported so the control layer
// can report and point the OS proxy at the same effective address the builder
// emits. Chosen away from the clash API's 9090.
const DefaultMixedPort = 2080

// Connection modes. ModeTun carries all traffic through a tun device with
// auto_route (the default, needs the tun driver). ModeSystemProxy skips the tun
// entirely and exposes a loopback mixed inbound the client wires up as the OS
// proxy — the path for locked-down machines where installing or running a tun is
// not permitted.
const (
	ModeTun         = "tun"
	ModeSystemProxy = "system-proxy"
	defaultConnMode = ModeTun
)

// ValidMode reports whether s names a connection mode Build accepts. The empty
// string is not valid here — callers that mean "default" should leave the option
// unset and let normalize fill it, mirroring ValidStack.
func ValidMode(s string) bool {
	switch s {
	case ModeTun, ModeSystemProxy:
		return true
	}
	return false
}

// IsSystemProxy reports whether these options select the system-proxy inbound
// (a loopback mixed listener) rather than the tun. It reads the raw Mode, so an
// unset Mode is tun — the same default normalize applies — letting the control
// layer decide whether to arm the OS proxy from an un-normalized snapshot.
func (t TunOptions) IsSystemProxy() bool { return t.Mode == ModeSystemProxy }

// MixedHostPort returns the loopback host:port the mixed inbound listens on,
// applying DefaultMixedPort when none is set. It is the exact address the client
// points the OS proxy at, kept here so the port default lives next to the
// inbound that binds it and the two can never drift.
func (t TunOptions) MixedHostPort() string {
	port := t.MixedPort
	if port == 0 {
		port = DefaultMixedPort
	}
	return net.JoinHostPort(mixedListen, strconv.Itoa(port))
}

// The tun stacks sing-box implements. system uses the kernel's own TCP/IP
// (fastest, needs the tun driver to behave); gvisor runs a userspace stack
// (slower, but immune to driver quirks); mixed splits TCP onto system and UDP
// onto gvisor.
const (
	StackSystem = "system"
	StackGvisor = "gvisor"
	StackMixed  = "mixed"
)

// ValidStack reports whether s names a tun stack sing-box accepts. The empty
// string is not valid here — callers that mean "default" should leave the
// option unset and let normalize fill it.
func ValidStack(s string) bool {
	switch s {
	case StackSystem, StackGvisor, StackMixed:
		return true
	}
	return false
}

// TunOptions configures the single tun inbound and the clash API. The zero
// value is filled with defaults by Build.
type TunOptions struct {
	// Mode selects the inbound family: ModeTun (default) emits the tun inbound
	// below; ModeSystemProxy emits a loopback mixed inbound instead and none of the
	// tun/auto_route fields. It is named on TunOptions because the inbound is the
	// one thing the two modes swap; the rest of this struct (MTU/Stack/interface)
	// is read only in tun mode.
	Mode string
	// MixedPort is the loopback port the mixed inbound listens on in
	// ModeSystemProxy. Zero is filled with DefaultMixedPort by normalize; it is
	// inert in tun mode.
	MixedPort     int
	InterfaceName string
	MTU           int
	Stack         string // system, gvisor, or mixed
	// ExternalTun marks the tun device as owned by the host platform rather than
	// opened and routed by sing-box. It is the mobile case: the VPN file descriptor
	// is supplied by the OS (Android VpnService.Builder.establish, iOS
	// NEPacketTunnelProvider.packetFlow) and the OS installs the routes
	// (VpnService.Builder.addRoute, NEPacketTunnelNetworkSettings). sing-box must
	// not then also claim a default route, so the builder omits
	// auto_route/strict_route for this tun; MTU and Stack still apply, since they
	// shape the tun sing-box drives over the supplied fd. Off by default, so the
	// desktop path — where sing-box owns the tun and needs auto_route — is unchanged.
	ExternalTun  bool
	ClashAPIPort int
	// Address is the IPv4 CIDR the tun carries. Empty takes the historical
	// default. It is settable because that address is not ours alone — other VPN
	// clients hand their tun the same 172.19.0.1, and two adapters cannot hold one
	// address: the second to start fails to configure and its sing-box exits while
	// the app that launched it keeps reporting a healthy tunnel. See
	// FreeTunAddresses, which the control layer uses to pick a pair that is
	// actually free on the machine. Address6 moves with it: the IPv6 ULA collides
	// just as readily — that is the collision Windows actually reports ("set ipv6
	// address: The object already exists") — so fixing only IPv4 leaves the
	// failure exactly where it was.
	Address  string
	Address6 string
	// CacheDir is the directory sing-box's cache_file is written to. When empty,
	// the cache file is enabled without an explicit path and sing-box resolves it
	// against the process working directory — correct for the GUI sidecar. The
	// root launchd daemon on macOS runs with cwd "/", which is read-only, so it
	// passes its writable profile-store directory here to give cache.db an
	// absolute home; otherwise sing-box aborts at startup on every connect.
	CacheDir string
}

// DefaultTUNName is the interface name an unset TunOptions.InterfaceName
// resolves to on this platform (empty on macOS, where the kernel dictates
// utun<N>; the branded name elsewhere).
//
// Exported so callers that must reason about *our* interface before a config
// exists — the tun-conflict guard, which has to exclude our own tun from the
// interfaces it scans — read the name from the one place that assigns it,
// instead of re-deriving it and drifting.
func DefaultTUNName() string { return platformTUNName }

func (t TunOptions) normalize() TunOptions {
	if t.Mode == "" {
		t.Mode = defaultConnMode
	}
	if t.MixedPort == 0 {
		t.MixedPort = DefaultMixedPort
	}
	if t.InterfaceName == "" {
		// Empty stays empty on macOS (platformTUNName == ""), which tells sing-box
		// to claim the next free utun — the only names its kernel allows. Off
		// macOS this is the branded "tenebra".
		t.InterfaceName = platformTUNName
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

	// The global tls-fragment toggle forces fragmentation on every TLS-bearing
	// node, converging on the same per-node tls.fragment field the adaptive walk
	// sets, so buildNodes/tlsObject stay the single emission point.
	if ro.TLSFragment {
		nodes = forceFragment(nodes)
	}

	outs, endpoints, sel, err := buildNodes(nodes)
	if err != nil {
		return nil, err
	}
	if len(sel) == 0 {
		return nil, fmt.Errorf("singbox: no usable nodes")
	}
	tags := make([]string, len(sel))
	for i, nt := range sel {
		tags[i] = nt.Tag
	}

	// Resolve the selector default: honor selectedTag when present, else first.
	def := selectedTag
	if def == "" || !contains(tags, def) {
		def = tags[0]
	}
	selOutbounds := tags

	// Multihop rewires the topology into a two-hop chain: the exit outbound gets a
	// detour through the entry outbound, and the selector collapses to the exit so
	// the route final (still proxyTag) egresses via exit -> entry -> internet. It
	// engages only when both endpoints resolve to distinct regular outbounds this
	// config actually built — an AmneziaWG endpoint or a dropped/invalid node leaves
	// its tag out of outs — so a stale or unsupported selection degrades to the
	// normal selector rather than emitting a dangling detour, which sing-box accepts
	// at check time and then silently misroutes. sing-box's detour is a plain
	// top-level outbound field naming the tag to dial through (verified against the
	// bundled 1.13 schema).
	if ro.Multihop && ro.MultihopEntry != "" && ro.MultihopExit != "" && ro.MultihopEntry != ro.MultihopExit {
		_, entryOK := outboundByTag(outs, ro.MultihopEntry)
		exitObj, exitOK := outboundByTag(outs, ro.MultihopExit)
		if entryOK && exitOK {
			// The entry outbound needs no change: it is dialed as an ordinary
			// outbound and only referenced by the exit's detour.
			exitObj["detour"] = ro.MultihopEntry
			selOutbounds = []string{ro.MultihopExit}
			def = ro.MultihopExit
		}
	}

	// Shared outbounds: the selector over the eligible nodes, plus direct/block.
	selector := map[string]any{
		"type":      "selector",
		"tag":       proxyTag,
		"outbounds": selOutbounds,
		"default":   def,
	}
	outbounds := make([]map[string]any, 0, len(outs)+3)
	outbounds = append(outbounds, selector)
	outbounds = append(outbounds, outs...)
	outbounds = append(outbounds,
		map[string]any{"type": "direct", "tag": directTag},
		map[string]any{"type": "block", "tag": blockTag},
	)

	// A per-run secret gates the clash API's external controller. Without it any
	// local process could read the user's live connections (GET /connections) or
	// flip the selected outbound (PUT /proxies) over 127.0.0.1 — a browsing-
	// metadata leak on a privacy client. rand.Text draws from crypto/rand, so the
	// token is unguessable; the runner reads it back out of this config to
	// authenticate its own stats/probe calls.
	clashSecret := rand.Text()

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
		"inbounds":  []map[string]any{inbound(tun, ro.KillSwitch)},
		"outbounds": outbounds,
		"route":     routeBlock(ro),
		"experimental": map[string]any{
			"clash_api": map[string]any{
				"external_controller": fmt.Sprintf("127.0.0.1:%d", tun.ClashAPIPort),
				"secret":              clashSecret,
			},
			"cache_file": cacheFileBlock(tun.CacheDir),
		},
	}

	// Endpoints (WireGuard/AmneziaWG) live in their own top-level array in
	// sing-box 1.11+. Only emit the key when there's something in it.
	if len(endpoints) > 0 {
		cfg["endpoints"] = endpoints
	}

	return cfg, nil
}

// cacheFileBlock builds the experimental.cache_file object. The cache persists
// selector choices and DNS results across runs. With dir empty the file is
// enabled without a path, so sing-box resolves cache.db against its working
// directory (fine for the GUI sidecar); with dir set the cache is pinned to an
// absolute path there, which is what lets the root daemon — cwd "/" and
// read-only — keep a working cache instead of aborting at startup.
func cacheFileBlock(dir string) map[string]any {
	block := map[string]any{"enabled": true}
	if dir != "" {
		block["path"] = filepath.Join(dir, "cache.db")
	}
	return block
}

// forceFragment returns nodes with TLS fragmentation turned on for every
// TLS-bearing node — the global tls-fragment toggle. A node without a TLS block
// (Shadowsocks, WireGuard, or a Trojan/Hysteria2 node relying on the builder's
// force-emit default) has no ClientHello to fragment through the model and is
// returned unchanged, matching the adaptive walk's own scope. The slice and each
// mutated node's TLS are copied so the caller's nodes — the connect loop reuses
// one slice across attempts — are never altered in place.
func forceFragment(nodes []model.Node) []model.Node {
	out := make([]model.Node, len(nodes))
	copy(out, nodes)
	for i := range out {
		if out[i].TLS == nil {
			continue
		}
		tls := *out[i].TLS
		tls.Fragment = true
		out[i].TLS = &tls
	}
	return out
}

// NodeTag pairs a node the builder emits into the selector with the tag Build
// assigns its outbound and the node's position in the input slice. It is what
// SelectorTags returns so a caller can map a selectable node back to both the
// exact selector tag and the profile server it came from.
type NodeTag struct {
	Index int
	Tag   string
}

// buildNodes converts nodes into outbounds and endpoints, assigning each a
// unique tag. It returns the outbound objects, endpoint objects, and the
// selectable nodes in input order (each with its tag and input index) for the
// selector. Zero/unknown protocols and semantically-invalid nodes (see
// validateNode) are skipped, freeing their tag, so one bad entry can't poison the
// shared config the connect path builds from every profile node. The error return
// is reserved for genuinely fatal conditions; today it is always nil and the
// caller turns an empty selection into the "no usable nodes" error.
func buildNodes(nodes []model.Node) (outs, endpoints []map[string]any, sel []NodeTag, err error) {
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

	for i, n := range nodes {
		if n.Protocol == "" {
			continue // skip zero-protocol entries
		}
		tag := uniq(n.Name)

		// A semantically-invalid node (bad port, missing credentials, keyless
		// REALITY) must not be emitted: the connect path builds one config from
		// every profile node, so a single node sing-box rejects at decode fails
		// the ENTIRE config — every selector member and fallback candidate with
		// it. Skip the offender exactly like an unknown protocol, freeing its tag,
		// so one bad entry doesn't sink a whole subscription.
		if err := validateNode(n); err != nil {
			delete(seen, tag)
			continue
		}

		if n.Protocol == model.AmneziaWG {
			ep, epErr := wireguardEndpoint(n, tag)
			if epErr != nil {
				// validateNode already vetted the key material; treat any residual
				// endpoint error as a skip, not a fatal, to keep the blast radius
				// to the single node.
				delete(seen, tag)
				continue
			}
			endpoints = append(endpoints, ep)
			sel = append(sel, NodeTag{Index: i, Tag: tag})
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
		sel = append(sel, NodeTag{Index: i, Tag: tag})
	}
	return outs, endpoints, sel, nil
}

// SelectorTags returns, in selector order, the nodes Build emits as selectable
// outbounds — each paired with the exact tag Build assigns it and its index in
// nodes. Nodes Build drops (zero/unknown protocol, or one that fails the same
// validation Build applies) are omitted, so the result is precisely the selector
// membership of the config Build produces from the same nodes. It exposes the
// builder's own tag assignment so a caller that must reason about the selector's
// tags — the mobile connect loop ordering them for its fallback walk — derives
// them from the single authority that mints them rather than re-deriving and
// drifting.
func SelectorTags(nodes []model.Node) []NodeTag {
	_, _, sel, _ := buildNodes(nodes)
	return sel
}

// validateNode reports whether a node carries the minimum fields sing-box needs
// to build a working outbound/endpoint for its protocol. It returns an error
// describing the first missing requirement; buildNodes turns that into a
// logged-skip rather than a fatal so one bad node can't poison the shared
// config. The checks mirror sing-box's own decode-time requirements:
//   - port must be a valid uint16 (1..65535), for every protocol;
//   - Shadowsocks needs a method AND a password, and must carry no transport
//     plugin — shadowsocksOutbound emits none of sing-box's plugin/plugin_opts
//     fields, so a plugin node would build a plain outbound that fails its
//     handshake in silence;
//   - Trojan and Hysteria2 need a password;
//   - VLESS and VMess need a UUID;
//   - a REALITY node (TLS.Reality set) needs a non-empty public_key — a keyless
//     reality outbound can never handshake and sing-box FATALs on it.
func validateNode(n model.Node) error {
	if n.Port < 1 || n.Port > 65535 {
		return fmt.Errorf("node %q: port %d out of range 1..65535", n.Name, n.Port)
	}
	if n.TLS != nil && n.TLS.Reality != nil && n.TLS.Reality.PublicKey == "" {
		return fmt.Errorf("node %q: reality without public_key", n.Name)
	}
	switch n.Protocol {
	case model.Shadowsocks:
		if n.Method == "" || n.Password == "" {
			return fmt.Errorf("node %q: shadowsocks needs method and password", n.Name)
		}
		// A plugin (v2ray-plugin, obfs, shadow-tls, ...) changes the wire protocol,
		// and shadowsocksOutbound emits no plugin/plugin_opts, so the node would
		// build a plain outbound and mismatch the server's handshake. Refuse it
		// outright: an honest "unsupported plugin" beats a tunnel that looks
		// connected but silently passes no traffic.
		if n.Extra["plugin"] != "" {
			return fmt.Errorf("node %q: shadowsocks plugin %q is not supported", n.Name, n.Extra["plugin"])
		}
	case model.Trojan:
		if n.Password == "" {
			return fmt.Errorf("node %q: trojan needs password", n.Name)
		}
	case model.Hysteria2:
		if n.Password == "" {
			return fmt.Errorf("node %q: hysteria2 needs password", n.Name)
		}
	case model.VLESS, model.VMess:
		if n.UUID == "" {
			return fmt.Errorf("node %q: %s needs uuid", n.Name, n.Protocol)
		}
	case model.AmneziaWG:
		if n.WireGuard == nil || n.WireGuard.PrivateKey == "" || n.WireGuard.PeerPublicKey == "" {
			return fmt.Errorf("node %q: amneziawg needs private_key and peer_public_key", n.Name)
		}
	}
	return nil
}

// ValidateNode reports whether a node carries the minimum fields sing-box needs
// to build a working outbound/endpoint for its protocol — the same semantic
// check buildNodes applies before emitting a node (see validateNode). It is the
// canonical, exported predicate so callers that mirror the builder's node walk
// (the control package's selector-tag and fallback-candidate computations) drop
// exactly the nodes the builder drops. Without a shared check those mirrors keep
// a tag for a node the builder skips, and the selector default then points at the
// wrong exit.
func ValidateNode(n model.Node) bool {
	return validateNode(n) == nil
}

// inbound renders the single inbound the config carries, dispatching on the
// connection mode. ModeSystemProxy yields a loopback mixed inbound (no tun, no
// auto_route, so nothing needs the tun driver or a route claim); every other
// mode — including the normalized default — yields the tun inbound. strictRoute
// is meaningful only to the tun path (it is the kill switch); the mixed inbound
// ignores it, since a kill switch is a tun/auto_route firewall concept with no
// equivalent when the OS merely points at a local proxy.
func inbound(t TunOptions, strictRoute bool) map[string]any {
	if t.Mode == ModeSystemProxy {
		return mixedInbound(t.MixedPort)
	}
	return tunInbound(t, strictRoute)
}

// mixedInbound renders the loopback mixed (HTTP + SOCKS) inbound used in
// ModeSystemProxy. It binds to 127.0.0.1 only so the proxy is never reachable
// off the machine, and carries no sniff/domain_strategy fields — sing-box 1.12+
// moved those onto route rule actions, and the builder's route block already
// owns routing — so this stays the minimal listener the OS proxy points at.
// Verified against the bundled sing-box 1.13 schema with `sing-box check`.
func mixedInbound(port int) map[string]any {
	return map[string]any{
		"type":        "mixed",
		"tag":         mixedTag,
		"listen":      mixedListen,
		"listen_port": port,
	}
}

// tunInbound renders the single tun inbound. auto_route always routes traffic
// into the tunnel; strict_route follows the kill-switch option. strict_route is
// what makes the kill switch real — sing-box installs firewall rules that drop
// any packet trying to escape the tunnel, so nothing leaks when the proxy is
// down. But those rules are applied system-wide the instant the tunnel comes up,
// which stalls every existing connection (and the desktop UI's own webview)
// while Windows reprograms its filter engine. That trade — a hard leak guarantee
// for a rough connect — is the user's to make, so it is off unless the
// kill-switch option is set.
func tunInbound(t TunOptions, strictRoute bool) map[string]any {
	in := map[string]any{
		"type":    "tun",
		"tag":     tunTag,
		"address": tunAddressesFor(t),
		"mtu":     t.MTU,
		"stack":   t.Stack,
	}
	// Who owns the routes decides whether sing-box claims them. On desktop sing-box
	// opens the tun itself and must install the system routes, so it emits
	// auto_route (and strict_route when the kill switch is armed). When the tun is
	// supplied and routed by the host platform (ExternalTun — the mobile
	// VpnService / Network Extension case), the OS already holds the routing table,
	// so emitting auto_route here would fight it; both are left off. sing-box emits
	// no auto_redirect in either case.
	if !t.ExternalTun {
		in["auto_route"] = true
		in["strict_route"] = strictRoute
	}
	// Only name the interface when there is a name to give. An empty name (the
	// macOS default) must be omitted, not sent as "", so sing-box auto-assigns a
	// utun device instead of rejecting the empty string.
	if t.InterfaceName != "" {
		in["interface_name"] = t.InterfaceName
	}
	return in
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

// outboundByTag returns the first outbound object in outs whose tag matches, and
// whether one was found. It locates the multihop exit outbound so its detour can
// be set, and confirms the entry is a regular outbound: AmneziaWG endpoints live
// in the separate endpoints array, not outs, so they can neither carry a detour
// nor be one here, and a lookup miss is what keeps multihop off for such a pick.
func outboundByTag(outs []map[string]any, tag string) (map[string]any, bool) {
	for _, o := range outs {
		if t, _ := o["tag"].(string); t == tag {
			return o, true
		}
	}
	return nil, false
}
