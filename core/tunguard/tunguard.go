// Package tunguard refuses to bring up a tun when another VPN already owns the
// machine's default route.
//
// Two tun interfaces with their own default route at the same metric is not a
// degraded state that resolves itself — it is a coin flip repeated per
// connection. Whichever route the stack picks, one tunnel's own outbound packets
// can be captured by the other and never reach their server, so the visible
// symptom is not "the new VPN is slow" but "the machine lost the internet",
// including for the tunnel that was working a second earlier.
//
// This was observed twice on 2026-08-18 on the author's machine: a client whose
// service restarted itself raised a second tun beside the running one, and every
// request began timing out — while both tunnels reported themselves connected
// and every node passed a TCP ping. Diagnosing it from inside either app was
// near-impossible; refusing to create the second tun in the first place is.
//
// The guard is deliberately advisory-with-teeth: it names the conflicting
// interface so the user can act, and it can be overridden explicitly, but the
// default is to refuse. Silently proceeding is what produced the incident.
package tunguard

import (
	"fmt"
	"strings"
)

// Iface is one network interface as seen by the platform adapter. Only the
// fields the guard reasons about are modelled; adapters fill them from whatever
// the OS offers (GetIpForwardTable2 on Windows, netlink on Linux, route on
// macOS).
type Iface struct {
	// Name is the OS-visible interface name, e.g. "vpnfix", "tun0", "utun4".
	Name string
	// Description is the driver's own description of the adapter, when the
	// platform can supply one: "TAP-Windows Adapter V9", "Tailscale Tunnel",
	// "Intel(R) Ethernet Connection I219-V". Empty where the OS offers none.
	//
	// It exists because a tunnel's name is not reliably tunnel-shaped. OpenVPN's
	// TAP adapter arrives on Windows as plain "Ethernet 2" — no name list can
	// ever catch that, and the adapter is holding the default route. The
	// description is where the vendor writes down what the thing actually is.
	Description string
	// IsTunnel reports whether the adapter recognised this as a tunnel/virtual
	// interface. Adapters that can determine this from the driver should set it;
	// the guard falls back to name heuristics when they cannot.
	IsTunnel bool
	// HasDefault4 and HasDefault6 report whether the interface can capture the
	// whole IPv4 / IPv6 address space. That is a 0.0.0.0/0 or ::/0 route, but also
	// the split pair 0.0.0.0/1 + 128.0.0.0/1 (::/1 + 8000::/1), or any other set
	// of prefixes that tiles the family while sitting more specific than a default
	// route — the adapter is responsible for recognising all of them. The two
	// families are tracked apart because a tunnel can take one and leave the
	// other: one that owns only ::/0 captures every AAAA-resolved destination on a
	// dual-stack machine and is a conflict as real as one owning 0.0.0.0/0. An
	// interface without such coverage in a family is not a conflict there, however
	// tunnel-like it looks.
	HasDefault4 bool
	HasDefault6 bool
	// Metric4 and Metric6 are how the stack ranks that coverage, per family: kept
	// for the message so the user can see which path wins, and for the "parked at
	// a losing metric" comparison. They are only ever compared within a family —
	// the stack ranks IPv4 routes against IPv4 and IPv6 against IPv6, never across,
	// so a v4 number and a v6 number are two different scales, and reducing them to
	// one made a tunnel winning on IPv4 read as parked because the machine's IPv6
	// uplink cost less. Adapters report the effective figure `route print` and `ip
	// route` show: on Windows the route's own metric plus the interface metric, not
	// the route metric alone — every tunnel writes its route at metric 0, so the
	// route metric on its own says nothing about which path wins.
	Metric4 int
	Metric6 int
}

// hasDefault reports whether the interface holds a default route in either
// family — the precondition for being a conflict at all.
func (i Iface) hasDefault() bool { return i.HasDefault4 || i.HasDefault6 }

// familyDefault returns whether the interface captures one address family and the
// metric it does so at. v6 selects IPv6, otherwise IPv4. This is the accessor the
// per-family conflict comparison uses; nothing about it crosses families.
func (i Iface) familyDefault(v6 bool) (bool, int) {
	if v6 {
		return i.HasDefault6, i.Metric6
	}
	return i.HasDefault4, i.Metric4
}

// BestMetric returns the effective metric of the best family this interface can
// capture in full, and whether it captures any.
//
// IPv4 and IPv6 metrics are not comparable, so this collapses them to the lower
// of the two only for callers that genuinely need one representative figure — the
// physical-uplink picker and the conflict message. The per-family conflict
// decision never uses it: that path compares v4 against v4 and v6 against v6.
func (i Iface) BestMetric() (int, bool) {
	switch {
	case i.HasDefault4 && i.HasDefault6:
		if i.Metric6 < i.Metric4 {
			return i.Metric6, true
		}
		return i.Metric4, true
	case i.HasDefault4:
		return i.Metric4, true
	case i.HasDefault6:
		return i.Metric6, true
	default:
		return 0, false
	}
}

// tunnelPatterns are fragments that mean "tunnel" in an interface name or in the
// driver description behind it. Used when the adapter could not classify the
// interface itself, which on Windows is most of them.
//
// A false positive costs a spurious refusal on a machine with no VPN on it, so
// nothing broad goes in here: "virtual" would catch Hyper-V's vEthernet, which
// is the physical uplink on the author's machine. A false negative costs the
// whole guard, which is what the first list did — everything in the third group
// below was live on a machine that reported "all clear" while another client
// held the default route.
//
// Our own tun is deliberately not in the list, even though leaving it
// unrecognised is what let it be counted as a physical uplink holding the route
// at metric 0, dragging the bar every foreign tunnel is measured against down to
// 0 and making each of them look parked. The fix for that is ownNames, threaded
// through Conflicts and uplinkMetric — not a brand fragment. A substring rule
// for "tenebra" classifies the Hyper-V external switch on the author's machine,
// "vEthernet (TenebraExt)", as a tunnel, and that adapter is the machine's
// actual internet connection: the guard would then refuse every connect on a
// machine with no other VPN on it at all.
var tunnelPatterns = []string{
	// Generic virtual-adapter vocabulary. "tun" also covers every driver whose
	// description ends in "Tunnel": Tailscale's, WireGuard's, sing-box's.
	"tun", "utun", "tap", "wg", "wintun", "vpn", "ipsec",
	// Engines that ship an adapter under their own name.
	"sing", "nekoray", "hiddify", "clash", "xray", "amnezia",
	// Vendors whose adapter name carries no generic word at all. Measured live
	// 2026-08-24: not one of these was recognised, and both a Tailscale exit node
	// and NordLynx hold the default route while running.
	"tailscale", "nordlynx", "cloudflare", "warp", "mullvad", "zerotier",
	"netbird", "twingate", "wireguard", "openvpn",
}

// matchesTunnelPattern reports whether a name or a description carries a known
// tunnel fragment. Matching is case-insensitive and on substrings, because
// vendors ship names like "sing-tun Tunnel", "VpnFix" and "Hiddify Tunnel".
func matchesTunnelPattern(s string) bool {
	n := strings.ToLower(s)
	for _, p := range tunnelPatterns {
		if strings.Contains(n, p) {
			return true
		}
	}
	return false
}

// IsTunnelIface reports whether this interface is a tunnel: what the adapter
// said, or failing that what its name or its driver description looks like.
//
// Exported because the heuristic is not optional equipment. An adapter that
// leaves IsTunnel false on everything — as this one did before it learnt to read
// Windows' IfType — makes a caller that trusts the field alone treat another
// VPN's adapter as the machine's physical uplink. That is not hypothetical: it
// is how the bypass ended up pinned to the tunnel it was supposed to stay off.
//
// The description is read alongside the name because no list of names can win on
// its own: OpenVPN's TAP adapter is called "Ethernet 2".
func IsTunnelIface(ifc Iface) bool {
	return ifc.IsTunnel || matchesTunnelPattern(ifc.Name) || matchesTunnelPattern(ifc.Description)
}

// isTunnel is the internal spelling, kept so this package reads as it did.
func isTunnel(ifc Iface) bool { return IsTunnelIface(ifc) }

// lowerOwn lowercases the caller's own interface names, dropping the empties
// (macOS, where the kernel names the tun and there is nothing to exclude).
func lowerOwn(names []string) []string {
	own := make([]string, 0, len(names))
	for _, n := range names {
		if n != "" {
			own = append(own, strings.ToLower(n))
		}
	}
	return own
}

// isOwn reports whether an interface is one the caller already owns.
//
// Matching is by case-insensitive prefix rather than equality because Windows
// appends a suffix when an adapter name is taken: a tun raised beside one that
// has not finished going away comes up as "tenebra 2", and compared for equality
// against the name we asked for it reads as somebody else's VPN. Same rule as
// control.defaultIfacePresent and control.physicalIfaceIndex, which learnt it
// the same way.
func isOwn(name string, own []string) bool {
	n := strings.ToLower(name)
	for _, o := range own {
		if strings.HasPrefix(n, o) {
			return true
		}
	}
	return false
}

// uplinkMetric is the best (lowest) default-route metric in one address family
// among the interfaces that are neither tunnels nor ours — how good the machine's
// ordinary internet path is for that family. Returns false when no physical
// uplink carries a default route there.
//
// The family is a parameter because metrics do not cross it: a v4 tunnel must be
// measured against the v4 uplink, not against whatever the machine's v6 path
// happens to cost. Collapsing the two was its own bug — a tunnel winning on IPv4
// read as "parked" because the machine had a lower-numbered IPv6 uplink.
//
// Our own interfaces are excluded for the same reason foreign tunnels are, and it
// matters more: our tun holds the default route at metric 0 by construction, so
// counting it as the uplink sets the bar every foreign tunnel is compared against
// to 0, and each of them is then waved through as "parked at a losing metric".
// The guard would go blind the moment it started working. The same happens for
// any tunnel the classifier fails to recognise, which is why the pattern list
// above is not optional decoration. own excludes the case the classifier gets
// right but the interface is ours — its match is by prefix, so a user-set
// TunOptions.InterfaceName and the "<name> 2" suffix Windows appends when the
// first is still going away are both recognised as ours, not as an uplink whose
// metric-0 route would zero the bar.
func uplinkMetric(ifaces []Iface, own []string, v6 bool) (int, bool) {
	best, found := 0, false
	for _, ifc := range ifaces {
		has, metric := ifc.familyDefault(v6)
		if !has || isTunnel(ifc) || isOwn(ifc.Name, own) {
			continue
		}
		if !found || metric < best {
			best, found = metric, true
		}
	}
	return best, found
}

// Conflicts returns the interfaces that would fight our tun for the default
// route: tunnel-like, carrying a default route, not our own, and actually
// preferred by the routing stack.
//
// That last condition is what keeps the guard usable. Some VPNs park a
// default route at a deliberately terrible metric so it only applies as a last
// resort — Radmin VPN on the author's machine sits at metric 9257 against the
// NIC's 25, and Windows will never route through it. Refusing to connect because
// of such an entry is a false alarm the user cannot act on, and a guard that
// cries wolf gets switched off, taking the real protection with it. So a tunnel
// counts as a conflict only when its metric is at least as good as the physical
// uplink's, meaning it can genuinely win the route. With no physical uplink
// carrying a default route, every foreign tunnel qualifies — there is nothing
// else for it to lose to.
//
// The comparison is made per address family: the machine's v4 and v6 uplinks are
// found separately, and a tunnel is a conflict if it can win the route in either
// family it captures. Mixing the two hid conflicts both ways — a tunnel winning
// on IPv4 looked parked next to a cheaper IPv6 uplink, and a tunnel owning only
// ::/0 was invisible because it had no IPv4 route to compare at all.
//
// ownNames lists the interface names this process already owns (its current tun,
// and the name it is about to create), matched case-insensitively — reconnecting
// must not be blocked by the tunnel being replaced.
func Conflicts(ifaces []Iface, ownNames ...string) []Iface {
	own := lowerOwn(ownNames)
	uplink4, hasUplink4 := uplinkMetric(ifaces, own, false)
	uplink6, hasUplink6 := uplinkMetric(ifaces, own, true)

	var out []Iface
	for _, ifc := range ifaces {
		if !ifc.hasDefault() {
			continue // cannot capture traffic; not a conflict
		}
		if isOwn(ifc.Name, own) {
			continue
		}
		if !isTunnel(ifc) {
			continue // a physical uplink's default route is normal, not a conflict
		}
		if winsRoute(ifc, false, hasUplink4, uplink4) || winsRoute(ifc, true, hasUplink6, uplink6) {
			out = append(out, ifc)
		}
	}
	return out
}

// winsRoute reports whether a tunnel can actually be chosen by the stack for one
// address family: it holds a default route there, and either no physical uplink
// carries one in that family, or the tunnel's metric is at least as good as the
// uplink's. Ties go to "conflict" — at an equal metric the stack's choice is a
// coin flip, which is the exact state that takes the machine offline.
func winsRoute(ifc Iface, v6, hasUplink bool, uplink int) bool {
	has, metric := ifc.familyDefault(v6)
	if !has {
		return false // does not capture this family; nothing to win
	}
	return !hasUplink || metric <= uplink
}

// ErrConflict is returned by Check when another tunnel holds the default route.
// It carries the offenders so a caller can render them without re-scanning.
type ErrConflict struct {
	Conflicts []Iface
}

// Error names the conflicting interfaces and says what to do, because the
// failure this prevents is invisible from inside the app: the user sees "no
// internet", not "two default routes".
func (e *ErrConflict) Error() string {
	names := make([]string, len(e.Conflicts))
	for i, c := range e.Conflicts {
		if m, ok := c.BestMetric(); ok && m != 0 {
			names[i] = fmt.Sprintf("%s (metric %d)", c.Name, m)
		} else {
			names[i] = c.Name
		}
	}
	return fmt.Sprintf(
		"another VPN already owns the default route: %s. Two tunnels routing everything "+
			"take the machine offline rather than sharing. Disconnect it first, or override "+
			"this check if you know the routes do not overlap",
		strings.Join(names, ", "),
	)
}

// Is lets errors.Is match any *ErrConflict, so callers can branch on the kind of
// failure without depending on the offender list.
func (e *ErrConflict) Is(target error) bool {
	_, ok := target.(*ErrConflict)
	return ok
}

// Check reports whether it is safe to raise a tun right now.
//
// override exists because the guard cannot see intent: a user deliberately
// running a split-scope tunnel that does not claim 0.0.0.0/0, or debugging, has
// a legitimate reason to proceed. It must be an explicit user action, never a
// default — the whole point is that the failure mode is silent, so an
// automatically-set escape hatch would restore the original bug.
func Check(ifaces []Iface, override bool, ownNames ...string) error {
	if override {
		return nil
	}
	if c := Conflicts(ifaces, ownNames...); len(c) > 0 {
		return &ErrConflict{Conflicts: c}
	}
	return nil
}
