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
	if _, ok := got[coverKey{index: 7, family: testV4}]; !ok {
		t.Fatalf("defaults = %v, want interface 7 holding a default route", got)
	}
}

// TestSplitDefaultPairWorksOverIPv6: the guard documents ::/0 coverage, and the
// same trick applies one family over.
func TestSplitDefaultPairWorksOverIPv6(t *testing.T) {
	tbl := coverTable{}
	tbl.add(7, testV6, v6zero, 1, 0)
	tbl.add(7, testV6, v6half, 1, 0)

	if _, ok := tbl.defaults()[coverKey{index: 7, family: testV6}]; !ok {
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

	if got := tbl.defaults()[coverKey{index: 7, family: testV4}]; got != 25 {
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

// TestDefaultsReportsTheBestMetricPerFamily: an interface with several ways to
// capture a family is as good as its best route in that family — the metric the
// routing stack compares against the others. The two families are reported
// separately: a low IPv6 number must not stand in for the IPv4 metric a v4 tunnel
// would be measured against.
func TestDefaultsReportsTheBestMetricPerFamily(t *testing.T) {
	tbl := coverTable{}
	tbl.add(7, testV4, v4zero, 0, 40)
	tbl.add(7, testV4, v4zero, 0, 5)
	tbl.add(7, testV6, v6zero, 0, 60)

	got := tbl.defaults()
	if m := got[coverKey{index: 7, family: testV4}]; m != 5 {
		t.Errorf("v4 metric = %d, want the best (5)", m)
	}
	if m := got[coverKey{index: 7, family: testV6}]; m != 60 {
		t.Errorf("v6 metric = %d, want 60 (not collapsed into the v4 figure)", m)
	}
}

// TestFourQuartersTileTheSpace is the gap the honest coverage check exists to
// close: a machine taken over by 0.0.0.0/2 + 64.0.0.0/2 + 128.0.0.0/2 +
// 192.0.0.0/2 has every address routed by the interface, yet not one of those
// routes is /0 or a /1 half. A check that only knew those shapes reported "all
// clear" for a client carrying 100% of the traffic.
func TestFourQuartersTileTheSpace(t *testing.T) {
	tbl := coverTable{}
	for _, q := range [][]byte{{0, 0, 0, 0}, {64, 0, 0, 0}, {128, 0, 0, 0}, {192, 0, 0, 0}} {
		tbl.add(7, testV4, q, 2, 0)
	}
	if _, ok := tbl.defaults()[coverKey{index: 7, family: testV4}]; !ok {
		t.Fatal("four /2 routes covering the whole space not recognised as a default route")
	}
}

// TestMixedPrefixLengthsTileTheSpace: coverage is a property of the set, not of a
// single shape, so a half plus two quarters covers exactly as well as the /1 pair.
func TestMixedPrefixLengthsTileTheSpace(t *testing.T) {
	tbl := coverTable{}
	tbl.add(7, testV4, []byte{0, 0, 0, 0}, 1, 0)   // lower half
	tbl.add(7, testV4, []byte{128, 0, 0, 0}, 2, 0) // third quarter
	tbl.add(7, testV4, []byte{192, 0, 0, 0}, 2, 0) // fourth quarter
	if _, ok := tbl.defaults()[coverKey{index: 7, family: testV4}]; !ok {
		t.Fatal("/1 + /2 + /2 covering the whole space not recognised")
	}
}

// TestThreeQuartersIsNotCoverage: leave one quarter and the tunnel is scoped, not
// a default route — the guard must let it through, since it cannot swallow our
// packets. This is the boundary the four-quarter case sits just past.
func TestThreeQuartersIsNotCoverage(t *testing.T) {
	tbl := coverTable{}
	for _, q := range [][]byte{{0, 0, 0, 0}, {64, 0, 0, 0}, {128, 0, 0, 0}} {
		tbl.add(7, testV4, q, 2, 0)
	}
	if got := tbl.defaults(); len(got) != 0 {
		t.Fatalf("defaults = %v, want none — a quarter of the space is still uncovered", got)
	}
}

// TestSpecificRouteDoesNotPoisonTheDefaultMetric: an interface holding a real
// default route at metric 25 and a LAN /24 at a wonderful metric 1 must be
// reported at 25, not 1. Taking the minimum metric over every route would let the
// /24 masquerade as a metric-1 default route — and a physical uplink read as
// metric 1 drags the bar every foreign tunnel is measured against down with it,
// waving the real conflicts through as "parked".
func TestSpecificRouteDoesNotPoisonTheDefaultMetric(t *testing.T) {
	tbl := coverTable{}
	tbl.add(7, testV4, v4zero, 0, 25)                 // the real default route
	tbl.add(7, testV4, []byte{192, 168, 1, 0}, 24, 1) // a LAN route at a great metric

	if got := tbl.defaults()[coverKey{index: 7, family: testV4}]; got != 25 {
		t.Fatalf("metric = %d, want 25 — the /24 must not speak for the default route", got)
	}
}

// TestFourQuartersReportTheThresholdMetric: when the space is only fully covered
// once the worst of the tiling routes is included, that worst metric is the one
// the interface captures everything at. Three quarters at 5 and the last at 30
// means all traffic is only caught at 30.
func TestFourQuartersReportTheThresholdMetric(t *testing.T) {
	tbl := coverTable{}
	tbl.add(7, testV4, []byte{0, 0, 0, 0}, 2, 5)
	tbl.add(7, testV4, []byte{64, 0, 0, 0}, 2, 5)
	tbl.add(7, testV4, []byte{128, 0, 0, 0}, 2, 5)
	tbl.add(7, testV4, []byte{192, 0, 0, 0}, 2, 30)

	if got := tbl.defaults()[coverKey{index: 7, family: testV4}]; got != 30 {
		t.Fatalf("metric = %d, want 30 — the last quarter is needed for full coverage", got)
	}
}
