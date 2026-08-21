package zapret

import (
	"strconv"
	"strings"
)

// PinInterface confines the packet filter to one network interface and reports
// whether anything changed.
//
// Without it, winws filters every interface on the machine — including the
// tunnel's own adapter. Inside a tun the traffic is still plain TLS, so the
// desync fires a second time on the very ClientHello sing-box is about to read,
// and sing-box's stack (unlike the router the fake packet was crafted to fool)
// accepts it. The stream is then garbage: sniffing times out, the domain rule
// that would have sent YouTube down the direct path never matches, and the
// connection falls through to the tunnel and hangs there.
//
// Measured on a live machine, both directions: with the filter unpinned, a
// browser reached YouTube and Discord through the tunnel not at all (curl never
// finished the handshake, the tunnel log showed a flat 300ms sniff timeout on
// every attempt) while everything absent from zapret's host lists was fine.
// Bound to the physical adapter, both came back. The bypass still does its job —
// it is applied where the packets actually meet the DPI, and nowhere else.
//
// The index is WinDivert's interface index (Windows ifIndex). A non-positive
// index means "not known", and the batch is returned untouched: filtering
// everything is wrong, but filtering a guessed interface is worse — it would
// silently bypass nothing at all.
func PinInterface(bat string, ifIndex int) (string, bool) {
	if ifIndex <= 0 {
		return bat, false
	}
	lines := strings.Split(bat, "\n")
	changed := false

	for i, line := range lines {
		// The launch line is the one that starts winws with a capture filter.
		// Strategy batches build everything else — lists, game filters — around it.
		if !strings.Contains(line, "winws.exe") || !strings.Contains(line, "--wf-") {
			continue
		}
		if strings.Contains(line, "--wf-iface=") {
			continue // already pinned, by us or by the bundle
		}
		at := strings.Index(line, "--wf-")
		if at < 0 {
			continue
		}
		lines[i] = line[:at] + "--wf-iface=" + strconv.Itoa(ifIndex) + " " + line[at:]
		changed = true
	}

	if !changed {
		return bat, false
	}
	return strings.Join(lines, "\n"), true
}
