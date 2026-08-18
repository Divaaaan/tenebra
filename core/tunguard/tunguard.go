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
	// IsTunnel reports whether the adapter recognised this as a tunnel/virtual
	// interface. Adapters that can determine this from the driver should set it;
	// the guard falls back to name heuristics when they cannot.
	IsTunnel bool
	// HasDefaultRoute reports whether the interface carries a 0.0.0.0/0 (or
	// ::/0) route. An interface without one cannot capture arbitrary traffic and
	// is therefore not a conflict, however tunnel-like it looks.
	HasDefaultRoute bool
	// RouteMetric is the metric of that default route, kept for the message so
	// the user can see which one the stack prefers.
	RouteMetric int
}

// tunnelPrefixes are interface-name patterns that mean "tunnel" across the
// platforms Tenebra targets. Used only when the adapter could not classify the
// interface itself: a false positive here costs a spurious refusal, so the list
// stays to well-known virtual-adapter names rather than anything broad.
var tunnelPrefixes = []string{"tun", "utun", "tap", "wg", "wintun", "sing", "nekoray", "hiddify", "vpn"}

// looksLikeTunnel reports whether the interface name matches a known tunnel
// pattern. Matching is case-insensitive and on substrings, because vendors ship
// names like "sing-tun Tunnel", "VpnFix" and "Hiddify Tunnel".
func looksLikeTunnel(name string) bool {
	n := strings.ToLower(name)
	for _, p := range tunnelPrefixes {
		if strings.Contains(n, p) {
			return true
		}
	}
	return false
}

// Conflicts returns the interfaces that would fight our tun for the default
// route: tunnel-like, carrying a default route, and not our own.
//
// ownNames lists the interface names this process already owns (its current tun,
// and the name it is about to create), matched case-insensitively — reconnecting
// must not be blocked by the tunnel being replaced.
func Conflicts(ifaces []Iface, ownNames ...string) []Iface {
	own := make(map[string]struct{}, len(ownNames))
	for _, n := range ownNames {
		if n != "" {
			own[strings.ToLower(n)] = struct{}{}
		}
	}

	var out []Iface
	for _, ifc := range ifaces {
		if !ifc.HasDefaultRoute {
			continue // cannot capture traffic; not a conflict
		}
		if _, ours := own[strings.ToLower(ifc.Name)]; ours {
			continue
		}
		if !ifc.IsTunnel && !looksLikeTunnel(ifc.Name) {
			continue // a physical uplink's default route is normal, not a conflict
		}
		out = append(out, ifc)
	}
	return out
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
		if c.RouteMetric != 0 {
			names[i] = fmt.Sprintf("%s (metric %d)", c.Name, c.RouteMetric)
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
