package tunguard

import (
	"errors"
	"strings"
	"testing"
)

// physical is the machine's real uplink: it owns a default route, and that is
// entirely normal.
var physical = Iface{Name: "Ethernet", HasDefaultRoute: true, RouteMetric: 25}

func TestConflictsFindsTheSecondTunnel(t *testing.T) {
	// The 2026-08-18 layout: two sing-tun adapters, both holding a default route
	// at metric 0, plus the physical uplink. Connectivity died even though both
	// clients reported themselves connected.
	ifaces := []Iface{
		physical,
		{Name: "vpnfix", IsTunnel: true, HasDefaultRoute: true},
		{Name: "tun0", IsTunnel: true, HasDefaultRoute: true},
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
		{Name: "tenebra", IsTunnel: true, HasDefaultRoute: true},
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
		{Name: "Radmin VPN", HasDefaultRoute: true, RouteMetric: 9257},
		{Name: "tun0", IsTunnel: true, HasDefaultRoute: true, RouteMetric: 0},
	}

	got := Conflicts(ifaces, "tenebra")
	if len(got) != 1 || got[0].Name != "tun0" {
		t.Fatalf("Conflicts = %+v, want only tun0 (Radmin is parked at a losing metric)", got)
	}
}

func TestConflictsFlagsEveryTunnelWhenThereIsNoUplink(t *testing.T) {
	// With no physical default route there is nothing for a tunnel to lose to,
	// so even a high metric wins by default and must still be reported.
	ifaces := []Iface{{Name: "tun0", IsTunnel: true, HasDefaultRoute: true, RouteMetric: 9000}}
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
		{Name: "wg-corp", IsTunnel: true, HasDefaultRoute: false},
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
			got := Conflicts([]Iface{{Name: name, HasDefaultRoute: true}})
			if len(got) != 1 {
				t.Fatalf("Conflicts(%q) = %+v, want it flagged", name, got)
			}
		})
	}
	// And a plain NIC name must not be dragged in by the heuristic.
	for _, name := range []string{"Ethernet", "Wi-Fi", "eth0", "en0"} {
		t.Run(name, func(t *testing.T) {
			got := Conflicts([]Iface{{Name: name, HasDefaultRoute: true}})
			if len(got) != 0 {
				t.Fatalf("Conflicts(%q) = %+v, want it ignored", name, got)
			}
		})
	}
}

func TestCheckRefusesAndExplains(t *testing.T) {
	ifaces := []Iface{
		physical,
		{Name: "tun0", IsTunnel: true, HasDefaultRoute: true, RouteMetric: 0},
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
	ifaces := []Iface{{Name: "tun0", IsTunnel: true, HasDefaultRoute: true}}
	if err := Check(ifaces, true, "tenebra"); err != nil {
		t.Fatalf("override did not pass: %v", err)
	}
	if err := Check(ifaces, false, "tenebra"); err == nil {
		t.Fatal("default must refuse")
	}
}
