package windows

import "testing"

// Address families, spelled as the two distinct buckets the table keeps apart.
const (
	testV4 uint16 = 2
	testV6 uint16 = 23
)

var (
	v4zero = []byte{0, 0, 0, 0}
	v4half = []byte{128, 0, 0, 0}
	v6zero = make([]byte, 16)
	v6half = append([]byte{0x80}, make([]byte, 15)...)
)

// TestSplitDefaultPairIsADefaultRoute is the blindness this table exists to fix.
// The standard way to take a machine over is not to touch 0.0.0.0/0 at all but
// to lay 0.0.0.0/1 + 128.0.0.0/1 over it — two routes that between them cover
// every address while each is more specific than the default route, so they win
// whatever its metric. It is what OpenVPN installs for `redirect-gateway def1`
// and what a Tailscale exit node installs. A test for a literal 0.0.0.0/0 sees
// neither of them.
func TestSplitDefaultPairIsADefaultRoute(t *testing.T) {
	tbl := coverTable{}
	tbl.add(7, testV4, v4zero, 1, 0)
	tbl.add(7, testV4, v4half, 1, 0)

	got := tbl.defaults()
	if _, ok := got[7]; !ok {
		t.Fatalf("defaults = %v, want interface 7 holding a default route", got)
	}
}

// TestSplitDefaultPairWorksOverIPv6: the guard documents ::/0 coverage, and the
// same trick applies one family over.
func TestSplitDefaultPairWorksOverIPv6(t *testing.T) {
	tbl := coverTable{}
	tbl.add(7, testV6, v6zero, 1, 0)
	tbl.add(7, testV6, v6half, 1, 0)

	if _, ok := tbl.defaults()[7]; !ok {
		t.Fatal("::/1 + 8000::/1 not recognised as full coverage")
	}
}

// TestHalfOfTheSplitPairIsNotFullCoverage: one half routes half the address
// space and leaves the other half alone. That is a scoped tunnel, which the
// guard deliberately does not block — refusing to connect over it would be a
// false alarm the user cannot act on.
func TestHalfOfTheSplitPairIsNotFullCoverage(t *testing.T) {
	tbl := coverTable{}
	tbl.add(7, testV4, v4zero, 1, 0)

	if got := tbl.defaults(); len(got) != 0 {
		t.Fatalf("defaults = %v, want none — half the address space is not a default route", got)
	}
}

// TestSplitHalvesDoNotCombineAcrossInterfaces: two interfaces each holding one
// half is a machine whose traffic is split between them, not one interface
// capturing everything.
func TestSplitHalvesDoNotCombineAcrossInterfaces(t *testing.T) {
	tbl := coverTable{}
	tbl.add(7, testV4, v4zero, 1, 0)
	tbl.add(8, testV4, v4half, 1, 0)

	if got := tbl.defaults(); len(got) != 0 {
		t.Fatalf("defaults = %v, want none — the halves belong to different interfaces", got)
	}
}

// TestSplitHalvesDoNotCombineAcrossFamilies: a v4 half plus a v6 half covers
// neither family.
func TestSplitHalvesDoNotCombineAcrossFamilies(t *testing.T) {
	tbl := coverTable{}
	tbl.add(7, testV4, v4zero, 1, 0)
	tbl.add(7, testV6, v6half, 1, 0)

	if got := tbl.defaults(); len(got) != 0 {
		t.Fatalf("defaults = %v, want none — the halves are in different address families", got)
	}
}

func TestLiteralDefaultRouteStillCounts(t *testing.T) {
	tbl := coverTable{}
	tbl.add(7, testV4, v4zero, 0, 25)

	if got := tbl.defaults()[7]; got != 25 {
		t.Fatalf("metric = %d, want 25", got)
	}
}

// TestRoutesThatMerelyLookShortAreIgnored: a one-bit prefix whose address is not
// the network address of that prefix is a different route that happens to be one
// bit long, and a /2 covers a quarter of the space however low its metric.
func TestRoutesThatMerelyLookShortAreIgnored(t *testing.T) {
	tbl := coverTable{}
	tbl.add(7, testV4, []byte{1, 2, 3, 4}, 1, 0)
	tbl.add(7, testV4, []byte{192, 0, 0, 0}, 1, 0)
	tbl.add(7, testV4, v4zero, 2, 0)
	tbl.add(7, testV4, []byte{1, 2, 3, 4}, 0, 0)

	if got := tbl.defaults(); len(got) != 0 {
		t.Fatalf("defaults = %v, want none", got)
	}
}

// TestDefaultsReportsTheBestMetric: an interface with several ways to capture
// everything is as good as its best one, which is the metric the routing stack
// will compare against the others.
func TestDefaultsReportsTheBestMetric(t *testing.T) {
	tbl := coverTable{}
	tbl.add(7, testV4, v4zero, 0, 40)
	tbl.add(7, testV4, v4zero, 0, 5)
	tbl.add(7, testV6, v6zero, 0, 60)

	if got := tbl.defaults()[7]; got != 5 {
		t.Fatalf("metric = %d, want the best (5)", got)
	}
}
