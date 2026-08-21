package zapret

import (
	"strings"
)

// derivedStrategyFile is the name the rewritten strategy is written under, inside
// the bundle directory.
//
// It has to live there and nowhere else: every shipped .bat resolves the binary
// and the host lists from its own location (`%~dp0bin\`, `%~dp0lists\`), so a copy
// anywhere else launches winws with no lists and silently bypasses nothing. The
// leading dot keeps it out of Discover's listing, so the derived file can never be
// offered as a strategy of its own or probed as if it were one.
const derivedStrategyFile = ".tenebra-strategy.bat"

// voiceUDPFilter is the port range the bundle's voice block claims: Discord's
// voice range plus the STUN range it uses to find its way there.
const voiceUDPFilter = "19294-19344,50000-50100"

// StripVoiceUDP removes the strategy's real-time-UDP block and reports whether
// anything changed.
//
// The block it removes looks like this, one line of a shipped strategy:
//
//	--filter-udp=19294-19344,50000-50100 --filter-l7=discord,stun
//	--dpi-desync=fake --dpi-desync-fake-discord=... --dpi-desync-fake-stun=... --new
//
// What it does is punch Discord's voice through the DPI on the *direct* path,
// which is exactly right for a machine with no tunnel — and exactly wrong for one
// with a tunnel carrying voice. WinDivert grabs those packets before they ever
// reach the tunnel's router, so the traffic leaves from the ISP address no matter
// what the routing table says; on a network where Discord's voice servers are
// blocked by address, the client then sits on "no route" forever while the
// routing rules insist the tunnel should have carried it.
//
// Measured on the author's network, both directions: bypass running, voice UDP
// exits the ISP address and voice never connects; bypass stopped, the same UDP
// exits the node and voice works — but YouTube dies, because the rest of the
// strategy went with it. Removing this one block is what lets both work at once.
//
// The port list is also taken out of the WinDivert capture filter (--wf-udp), so
// the packets are not intercepted at all rather than intercepted and passed
// through: an untouched packet cannot be reinjected on the wrong path.
//
// Everything else is left byte for byte. The other blocks are what carry YouTube.
func StripVoiceUDP(bat string) (string, bool) {
	lines := strings.Split(bat, "\n")
	out := make([]string, 0, len(lines))
	changed := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// The voice block is a whole line of the batch: drop it entirely. Dropping
		// the line also drops its trailing "--new ^" continuation, which is correct
		// — the block it separated is gone with it.
		if strings.HasPrefix(trimmed, "--filter-udp="+voiceUDPFilter) &&
			strings.Contains(trimmed, "--filter-l7=discord,stun") {
			changed = true
			continue
		}

		// Stop WinDivert from capturing those ports in the first place.
		if strings.Contains(line, "--wf-udp=") && strings.Contains(line, voiceUDPFilter) {
			line = strings.Replace(line, voiceUDPFilter+",", "", 1)
			changed = true
		}

		out = append(out, line)
	}

	if !changed {
		return bat, false
	}
	return strings.Join(out, "\n"), true
}
