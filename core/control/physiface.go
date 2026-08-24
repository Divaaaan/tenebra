package control

import (
	"net"
	"strings"

	"github.com/Divaaaan/tenebra/core/singbox"
	"github.com/Divaaaan/tenebra/core/tunguard"
)

// physicalIfaceIndex reports the index of the uplink the machine's own traffic
// actually leaves by — the interface the DPI bypass must be confined to.
//
// The bypass has to be pinned somewhere: left loose it also filters the tunnel's
// adapter, where its fake packets corrupt the very handshakes it is meant to
// protect (see zapret.PinInterface). But pinning it to the wrong interface is
// worse than not pinning it — a bypass that covers nothing looks exactly like a
// bypass that works until the user opens YouTube. So this answers only when the
// answer is unambiguous, and zero otherwise, which leaves today's behaviour.
//
// Unambiguous means: exactly one non-tunnel interface carrying a default route,
// or several where one has a strictly lower metric than the rest — the same
// preference the stack itself uses when choosing an exit.
func (d *Daemon) physicalIfaceIndex() int {
	if d.ifaceProbe == nil {
		return 0 // platform cannot tell us; do not guess
	}
	ifaces, err := d.ifaceProbe()
	if err != nil {
		return 0
	}

	// Our own tun by the name the builder gives it, every other tunnel by what the
	// platform says or what the name looks like. Reading i.IsTunnel alone is not
	// enough: the Windows adapter cannot classify interfaces, so it leaves that
	// field false on every one of them — and the first live run pinned the bypass
	// to the other VPN's adapter, which is the exact interface it must stay off.
	own := strings.ToLower(singbox.DefaultTUNName())

	var best tunguard.Iface
	bestMetric := 0
	found, tied := false, false
	for _, i := range ifaces {
		metric, hasDefault := i.BestMetric()
		if !hasDefault || tunguard.IsTunnelIface(i) {
			continue
		}
		if own != "" && strings.HasPrefix(strings.ToLower(i.Name), own) {
			continue
		}
		switch {
		case !found:
			best, bestMetric, found = i, metric, true
		case metric < bestMetric:
			best, bestMetric, tied = i, metric, false
		case metric == bestMetric:
			tied = true
		}
	}
	if !found || tied {
		return 0
	}
	return ifaceIndexByName(best.Name)
}

// pinAndProbeBind answers, from one lookup, the two questions a bypass pick must
// never answer differently: which interface the packet filter is confined to,
// and which interface the probes that score the strategies leave by.
//
// They used to be answered by two unrelated selectors with nothing comparing the
// results — this one, off the route table, and the socket-level list in
// selectDefaultInterface. Nothing made them agree: this one skips a foreign
// tunnel, while the other ranked interfaces by index, which says nothing about
// what an adapter is, so on a machine running another VPN it could take that
// tunnel. The filter would then sit on the uplink while the measurement went
// down somebody else's tunnel, where every target answers — baseline full marks,
// no strategy able to beat it, "nothing pierced the block". That is the symptom
// the pin was added to remove, arriving by a second route.
//
// Returning both from one place is the fix: there is one number, and the probe
// is bound to it or explicitly not bound at all. A zero pin means the filter is
// not confined either, so there is nothing to agree with and the bind falls back
// — see resolveZapretProbeBind.
func (d *Daemon) pinAndProbeBind() (int, zapretProbeBind) {
	pin := d.physicalIfaceIndex()
	return pin, resolveZapretProbeBind(pin, d.probeIfaces)
}

// ifaceIndexByName maps an interface name to its OS index, or 0 when no
// interface answers to that name. Matching is case-insensitive: the route table
// and the interface list do not always agree on capitalisation.
func ifaceIndexByName(name string) int {
	if name == "" {
		return 0
	}
	list, err := net.Interfaces()
	if err != nil {
		return 0
	}
	for _, i := range list {
		if strings.EqualFold(i.Name, name) {
			return i.Index
		}
	}
	return 0
}
