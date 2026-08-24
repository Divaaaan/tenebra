package tunguard

import (
	"errors"
	"strings"
	"testing"
)

// physical is the machine's real uplink: it owns a default route, and that is
// entirely normal.
var physical = Iface{Name: "Ethernet", HasDefault4: true, Metric4: 25}

func TestConflictsFindsTheSecondTunnel(t *testing.T) {
	// The 2026-08-18 layout: two sing-tun adapters, both holding a default route
	// at metric 0, plus the physical uplink. Connectivity died even though both
	// clients reported themselves connected.
	ifaces := []Iface{
		physical,
		{Name: "vpnfix", IsTunnel: true, HasDefault4: true},
		{Name: "tun0", IsTunnel: true, HasDefault4: true},
	}

	got := Conflicts(ifaces, "vpnfix")
	if len(got) != 1 || got[0].Name != "tun0" {
		t.Fatalf("Conflicts = %+v, want only tun0", got)
	}
}

func TestConflictsIgnoresOurOwnInterfaces(t *testing.T) {
	// Reconnecting replaces our own tun; being blocked by the interface we are
	// about to take over would make the guard unusable.
	ifaces := []Iface{
		physical,
		{Name: "tenebra", IsTunnel: true, HasDefault4: true},
	}
	if got := Conflicts(ifaces, "tenebra"); len(got) != 0 {
		t.Fatalf("Conflicts = %+v, want none (that is our own tun)", got)
	}
	// Names come from different OS APIs with different casing.
	if got := Conflicts(ifaces, "TENEBRA"); len(got) != 0 {
		t.Fatalf("case-insensitive match failed: %+v", got)
	}
}

func TestConflictsIgnoresTunnelParkedAtAWorseMetric(t *testing.T) {
	// Measured on the author's machine 2026-08-18: Radmin VPN keeps a default
	// route at metric 9257 against the NIC's 25, so Windows never routes through
	// it. Flagging that is a false alarm the user cannot act on — and a guard
	// that cries wolf gets switched off, taking the real protection with it.
	ifaces := []Iface{
		physical, // Ethernet, metric 25
		{Name: "Radmin VPN", HasDefault4: true, Metric4: 9257},
		{Name: "tun0", IsTunnel: true, HasDefault4: true, Metric4: 0},
	}

	got := Conflicts(ifaces, "tenebra")
	if len(got) != 1 || got[0].Name != "tun0" {
		t.Fatalf("Conflicts = %+v, want only tun0 (Radmin is parked at a losing metric)", got)
	}
}

func TestConflictsFlagsEveryTunnelWhenThereIsNoUplink(t *testing.T) {
	// With no physical default route there is nothing for a tunnel to lose to,
	// so even a high metric wins by default and must still be reported.
	ifaces := []Iface{{Name: "tun0", IsTunnel: true, HasDefault4: true, Metric4: 9000}}
	if got := Conflicts(ifaces, "tenebra"); len(got) != 1 {
		t.Fatalf("Conflicts = %+v, want tun0 flagged", got)
	}
}

func TestConflictsIgnoresPhysicalUplink(t *testing.T) {
	if got := Conflicts([]Iface{physical}); len(got) != 0 {
		t.Fatalf("Conflicts = %+v, want none — a NIC's default route is normal", got)
	}
}

func TestConflictsIgnoresTunnelWithoutDefaultRoute(t *testing.T) {
	// A split-scope tunnel routes only some prefixes; it cannot swallow our
	// packets, so it is not a conflict however tunnel-like its name is.
	ifaces := []Iface{
		physical,
		{Name: "wg-corp", IsTunnel: true, HasDefault4: false},
	}
	if got := Conflicts(ifaces); len(got) != 0 {
		t.Fatalf("Conflicts = %+v, want none — no default route", got)
	}
}

func TestConflictsFallsBackToNameWhenAdapterCannotClassify(t *testing.T) {
	// Adapters that cannot read the driver leave IsTunnel false; the name is
	// then the only signal, and vendors ship names like these.
	for _, name := range []string{"tun0", "utun4", "VpnFix", "Hiddify Tunnel", "sing-tun Tunnel", "wg0"} {
		t.Run(name, func(t *testing.T) {
			got := Conflicts([]Iface{{Name: name, HasDefault4: true}})
			if len(got) != 1 {
				t.Fatalf("Conflicts(%q) = %+v, want it flagged", name, got)
			}
		})
	}
	// And a plain NIC name must not be dragged in by the heuristic.
	for _, name := range []string{"Ethernet", "Wi-Fi", "eth0", "en0"} {
		t.Run(name, func(t *testing.T) {
			got := Conflicts([]Iface{{Name: name, HasDefault4: true}})
			if len(got) != 0 {
				t.Fatalf("Conflicts(%q) = %+v, want it ignored", name, got)
			}
		})
	}
}

func TestCheckRefusesAndExplains(t *testing.T) {
	ifaces := []Iface{
		physical,
		{Name: "tun0", IsTunnel: true, HasDefault4: true, Metric4: 0},
	}

	err := Check(ifaces, false, "tenebra")
	if err == nil {
		t.Fatal("Check passed while another tunnel owns the default route")
	}

	var conflict *ErrConflict
	if !errors.As(err, &conflict) {
		t.Fatalf("error %T, want *ErrConflict", err)
	}
	if len(conflict.Conflicts) != 1 || conflict.Conflicts[0].Name != "tun0" {
		t.Errorf("carried %+v, want tun0", conflict.Conflicts)
	}
	// The user sees "no internet", not "two default routes" — the message has to
	// name the offender for the error to be actionable.
	if !strings.Contains(err.Error(), "tun0") {
		t.Errorf("message does not name the offender: %s", err)
	}
	if !errors.Is(err, &ErrConflict{}) {
		t.Error("errors.Is could not match the conflict kind")
	}
}

func TestCheckPassesWhenAlone(t *testing.T) {
	if err := Check([]Iface{physical}, false, "tenebra"); err != nil {
		t.Fatalf("Check = %v, want nil when no other tunnel holds the route", err)
	}
}

func TestOverrideIsExplicitAndWins(t *testing.T) {
	// The escape hatch must work for the user who knows their routes do not
	// overlap — but it is only ever reachable by an explicit action, never a
	// default, since a silently-set override restores the original bug.
	ifaces := []Iface{{Name: "tun0", IsTunnel: true, HasDefault4: true}}
	if err := Check(ifaces, true, "tenebra"); err != nil {
		t.Fatalf("override did not pass: %v", err)
	}
	if err := Check(ifaces, false, "tenebra"); err == nil {
		t.Fatal("default must refuse")
	}
}

// TestConflictsFindsTunnelsWhoseNameSaysNothingGeneric: measured live on the
// author's machine 2026-08-24, where the guard reported "all clear" with three
// VPN adapters up. None of these names carries a "tun"/"vpn"/"wg" fragment, so
// a heuristic built only out of those reads them as ordinary network cards.
func TestConflictsFindsTunnelsWhoseNameSaysNothingGeneric(t *testing.T) {
	for _, name := range []string{"Tailscale", "NordLynx", "CloudflareWARP", "Mullvad", "ZeroTier One"} {
		t.Run(name, func(t *testing.T) {
			ifaces := []Iface{physical, {Name: name, HasDefault4: true, Metric4: 0}}
			if got := Conflicts(ifaces, "tenebra"); len(got) != 1 {
				t.Fatalf("Conflicts(%q) = %+v, want it flagged", name, got)
			}
		})
	}
}

// TestConflictsReadsTheAdapterDescription: OpenVPN's TAP adapter arrives on
// Windows as plain "Ethernet 2" — nothing in the name says VPN, and no name list
// can ever say otherwise. The driver's own description does.
func TestConflictsReadsTheAdapterDescription(t *testing.T) {
	ifaces := []Iface{
		physical,
		{Name: "Ethernet 2", Description: "TAP-Windows Adapter V9", HasDefault4: true},
	}
	got := Conflicts(ifaces, "tenebra")
	if len(got) != 1 || got[0].Name != "Ethernet 2" {
		t.Fatalf("Conflicts = %+v, want the TAP adapter flagged", got)
	}
}

// TestOrdinaryAdaptersStayOrdinary: a false positive here is a refusal to
// connect on a machine with no VPN on it at all, so the descriptions real
// network cards ship must not be dragged in by any of the fragments above.
func TestOrdinaryAdaptersStayOrdinary(t *testing.T) {
	cards := []Iface{
		{Name: "Ethernet", Description: "Intel(R) Ethernet Connection (7) I219-V"},
		{Name: "Wi-Fi", Description: "Intel(R) Wi-Fi 6E AX210 160MHz"},
		{Name: "Ethernet 2", Description: "Realtek PCIe GbE Family Controller"},
		{Name: "vEthernet (Ext)", Description: "Hyper-V Virtual Ethernet Adapter"},
		// A Hyper-V external switch on the author's machine, and the interface the
		// machine's traffic actually leaves by. Its name contains the product's
		// own name, which is why our tun is excluded by ownNames rather than by a
		// brand fragment in the pattern list.
		{Name: "vEthernet (TenebraExt)", Description: "Hyper-V Virtual Ethernet Adapter #2"},
		{Name: "Local Area Connection", Description: "ASIX AX88179 USB 3.0 to Gigabit Ethernet Adapter"},
		{Name: "Bluetooth Network Connection", Description: "Bluetooth Device (Personal Area Network)"},
		{Name: "eth0"},
		{Name: "en0"},
	}
	for _, c := range cards {
		t.Run(c.Name+"/"+c.Description, func(t *testing.T) {
			if IsTunnelIface(c) {
				t.Fatalf("%+v classified as a tunnel", c)
			}
		})
	}
}

// TestUnrecognisedTunnelDoesNotZeroTheUplink is the second-order failure, and
// the one that makes the guard worse than useless: a tunnel counted as a
// physical uplink brings the bar it is compared against down to its own metric
// of 0, and every genuine conflict at metric 1 or worse is then waved through as
// "parked at a losing metric". One invisible tunnel disarms the guard for all
// the visible ones.
func TestUnrecognisedTunnelDoesNotZeroTheUplink(t *testing.T) {
	ifaces := []Iface{
		physical, // Ethernet, metric 25
		// OpenVPN's TAP adapter: an ordinary-looking name, a default route, and
		// the metric 0 every tunnel installs.
		{Name: "Ethernet 2", Description: "TAP-Windows Adapter V9", HasDefault4: true, Metric4: 0},
		// A second VPN that does beat the physical uplink and must be reported.
		{Name: "Tailscale", HasDefault4: true, Metric4: 5},
	}

	got := Conflicts(ifaces, "tenebra")
	if len(got) != 2 {
		t.Fatalf("Conflicts = %+v, want both tunnels flagged", got)
	}
}

// TestOurOwnTunIsNotForeignWhenWindowsRenamesIt: Windows appends a suffix when
// an adapter name is already taken, so our second tun comes up as "tenebra 2".
// Compared for equality against the name we asked for, our own interface is
// reported as another VPN and the app refuses to reconnect to itself.
func TestOurOwnTunIsNotForeignWhenWindowsRenamesIt(t *testing.T) {
	ifaces := []Iface{
		physical,
		{Name: "tenebra 2", Description: "sing-tun Tunnel", HasDefault4: true, Metric4: 0},
	}
	if got := Conflicts(ifaces, "tenebra"); len(got) != 0 {
		t.Fatalf("Conflicts = %+v, want none — that is our own tun under a Windows suffix", got)
	}
	// And it must not be counted as the physical uplink either, or it drags the
	// comparison bar down to its own metric.
	ifaces = append(ifaces, Iface{Name: "Tailscale", HasDefault4: true, Metric4: 5})
	if got := Conflicts(ifaces, "tenebra"); len(got) != 1 || got[0].Name != "Tailscale" {
		t.Fatalf("Conflicts = %+v, want only Tailscale", got)
	}
}

// TestConflictsComparesWithinAddressFamily is the family-mixing bug (issue 2): a
// tunnel that wins the IPv4 route must be flagged even when the machine's IPv6
// uplink has a lower metric. Collapsing the two families into one number let the
// v6 figure (5) stand in for the v4 comparison, so a tunnel beating the v4 uplink
// (25) at metric 10 read as "parked at a losing metric" and sailed through.
func TestConflictsComparesWithinAddressFamily(t *testing.T) {
	ifaces := []Iface{
		// The physical uplink: an ordinary v4 metric, but a very low v6 one.
		{Name: "Ethernet", HasDefault4: true, Metric4: 25, HasDefault6: true, Metric6: 5},
		// A tunnel that owns IPv4 at a metric that beats the v4 uplink.
		{Name: "tun0", IsTunnel: true, HasDefault4: true, Metric4: 10},
	}
	got := Conflicts(ifaces, "tenebra")
	if len(got) != 1 || got[0].Name != "tun0" {
		t.Fatalf("Conflicts = %+v, want tun0 flagged: it wins the v4 route, and the v6 uplink metric is a different scale", got)
	}
}

// TestConflictsFindsAV6OnlyTunnel is issue 3: a tunnel that owns only ::/0
// captures every AAAA-resolved destination on a dual-stack machine — most of
// them. With the route table read for IPv4 only, or with no IPv6 uplink to
// compare against, it was invisible.
func TestConflictsFindsAV6OnlyTunnel(t *testing.T) {
	ifaces := []Iface{
		{Name: "Ethernet", HasDefault4: true, Metric4: 25},            // v4 uplink only
		{Name: "tun0", IsTunnel: true, HasDefault6: true, Metric6: 0}, // owns ::/0 only
	}
	got := Conflicts(ifaces, "tenebra")
	if len(got) != 1 || got[0].Name != "tun0" {
		t.Fatalf("Conflicts = %+v, want the ::/0 tunnel flagged; a v4 uplink does not cover for a missing v6 one", got)
	}
}

// TestConflictsIgnoresAV6TunnelParkedBehindAV6Uplink: the parked-metric rule
// holds per family too. A v6 tunnel the stack will never choose because the v6
// uplink is better is a false alarm, not a conflict.
func TestConflictsIgnoresAV6TunnelParkedBehindAV6Uplink(t *testing.T) {
	ifaces := []Iface{
		{Name: "Ethernet", HasDefault4: true, Metric4: 25, HasDefault6: true, Metric6: 25},
		{Name: "tun0", IsTunnel: true, HasDefault6: true, Metric6: 9000},
	}
	if got := Conflicts(ifaces, "tenebra"); len(got) != 0 {
		t.Fatalf("Conflicts = %+v, want none — the v6 tunnel loses to the v6 uplink", got)
	}
}

// TestConflictsFlagsATunnelThatWinsOnlyOneFamily: losing the route on IPv4 does
// not clear a tunnel that wins it on IPv6. Half the machine's traffic still goes
// to the wrong place, which is the whole failure the guard exists to stop.
func TestConflictsFlagsATunnelThatWinsOnlyOneFamily(t *testing.T) {
	ifaces := []Iface{
		{Name: "Ethernet", HasDefault4: true, Metric4: 25, HasDefault6: true, Metric6: 25},
		// Parked on v4 (9000 > 25), but beats the v6 uplink (5 < 25).
		{Name: "tun0", IsTunnel: true, HasDefault4: true, Metric4: 9000, HasDefault6: true, Metric6: 5},
	}
	got := Conflicts(ifaces, "tenebra")
	if len(got) != 1 || got[0].Name != "tun0" {
		t.Fatalf("Conflicts = %+v, want tun0 flagged: it wins the v6 route", got)
	}
}

// TestUplinkExcludesOurOwnTunUnderACustomName is the ownNames-in-uplinkMetric
// case (issue 5): our own tun is excluded from the physical-uplink metric by
// name, not only when it carries the hardcoded brand. A user-set InterfaceName
// with no tunnel fragment in it, holding the default route at metric 0 as every
// tun does, would otherwise be counted as a metric-0 uplink and drag the bar
// every foreign tunnel is measured against down to 0 — waving the real conflict
// through as "parked".
func TestUplinkExcludesOurOwnTunUnderACustomName(t *testing.T) {
	ifaces := []Iface{
		{Name: "Ethernet", HasDefault4: true, Metric4: 25},
		// Our own tun under a user-chosen name with no tunnel fragment, so only
		// ownNames can keep it from being read as a physical uplink.
		{Name: "corp-gw", HasDefault4: true, Metric4: 0},
		// A foreign tunnel that beats the real uplink and must still be flagged.
		{Name: "tun0", IsTunnel: true, HasDefault4: true, Metric4: 5},
	}
	got := Conflicts(ifaces, "corp-gw")
	if len(got) != 1 || got[0].Name != "tun0" {
		t.Fatalf("Conflicts = %+v, want only tun0 — our own custom-named tun must not zero the uplink bar", got)
	}
}

// TestOwnTunUnderCustomNameWithWindowsSuffix: the "<name> 2" rename applies to a
// custom InterfaceName too — Windows appends the suffix to whatever name we asked
// for, not only to the brand. Matched by prefix, "corp-gw 2" is still ours, and
// must be neither flagged nor counted as the uplink.
func TestOwnTunUnderCustomNameWithWindowsSuffix(t *testing.T) {
	ifaces := []Iface{
		{Name: "Ethernet", HasDefault4: true, Metric4: 25},
		{Name: "corp-gw 2", IsTunnel: true, HasDefault4: true, Metric4: 0},
	}
	if got := Conflicts(ifaces, "corp-gw"); len(got) != 0 {
		t.Fatalf("Conflicts = %+v, want none — that is our own tun under a Windows suffix", got)
	}
	// It must not be the uplink either: add a foreign tunnel and only it should show.
	ifaces = append(ifaces, Iface{Name: "tun0", IsTunnel: true, HasDefault4: true, Metric4: 5})
	if got := Conflicts(ifaces, "corp-gw"); len(got) != 1 || got[0].Name != "tun0" {
		t.Fatalf("Conflicts = %+v, want only tun0", got)
	}
}
