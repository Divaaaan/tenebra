package windows

import (
	"testing"

	winsys "golang.org/x/sys/windows"
)

// TestInterfacesReadsTheRealTable is an integration check against whatever
// machine the suite runs on: it cannot assert what is plugged in, but it can
// assert that the two iphlpapi calls parsed and that their results line up with
// net.Interfaces.
//
// A description on at least one adapter is the load-bearing part. It only
// appears if GetAdaptersAddresses returned, its linked list walked, and the
// adapter index it reports matched the index net.Interfaces uses — the join that
// carries IsTunnel and the interface metric across. Silent failure there leaves
// every interface looking like an unclassified metric-0 uplink, which is the
// state the guard cannot survive.
func TestInterfacesReadsTheRealTable(t *testing.T) {
	ifaces, err := Interfaces()
	if err != nil {
		t.Fatalf("Interfaces: %v", err)
	}
	if len(ifaces) == 0 {
		t.Fatal("no interfaces reported; a machine running this test has at least a loopback")
	}

	described := 0
	for _, i := range ifaces {
		if i.Name == "" {
			t.Errorf("interface reported with no name: %+v", i)
		}
		if i.Metric4 < 0 || i.Metric6 < 0 {
			t.Errorf("%s: negative metric (v4 %d, v6 %d)", i.Name, i.Metric4, i.Metric6)
		}
		// A metric only means something alongside a default route: an interface
		// with no coverage in a family must report 0 there, or the join that
		// carries the metric across landed on the wrong interface.
		if !i.HasDefault4 && i.Metric4 != 0 {
			t.Errorf("%s: v4 metric %d without a v4 default route", i.Name, i.Metric4)
		}
		if !i.HasDefault6 && i.Metric6 != 0 {
			t.Errorf("%s: v6 metric %d without a v6 default route", i.Name, i.Metric6)
		}
		if i.Description != "" {
			described++
		}
	}
	if described == 0 {
		t.Error("no adapter descriptions at all: the GetAdaptersAddresses join did not land")
	}
}

// TestAddMetricSumsRouteAndInterfaceMetric is the effective-metric rule (issue
// 4): Windows ranks a route by the route's own metric plus the owning interface's
// metric — the figure `route print` shows and the stack actually compares when it
// picks a path. Every tunnel writes its route at metric 0, and so does the
// physical uplink behind a Hyper-V switch (measured 2026-08-24: route metric 0,
// interface metric 25). Reading the route metric alone made the machine's own
// uplink look like a metric-0 path and, through the guard's parked-metric test,
// waved every genuine conflict through.
func TestAddMetricSumsRouteAndInterfaceMetric(t *testing.T) {
	if got := addMetric(0, 25); got != 25 {
		t.Errorf("addMetric(0, 25) = %d, want 25 (a metric-0 tun route on a metric-25 iface)", got)
	}
	if got := addMetric(10, 25); got != 35 {
		t.Errorf("addMetric(10, 25) = %d, want 35", got)
	}
}

// TestAddMetricSaturatesInsteadOfWrapping: a sum past uint32 clamps to the
// maximum rather than wrapping to a tiny number — a route nothing will ever
// choose must not come out looking like the best path on the machine.
func TestAddMetricSaturatesInsteadOfWrapping(t *testing.T) {
	maxU32 := ^uint32(0)
	if got := addMetric(maxU32, 100); got != maxU32 {
		t.Errorf("addMetric(max, 100) = %d, want %d (saturated, not wrapped)", got, maxU32)
	}
	if got := addMetric(maxU32-1, 5); got != maxU32 {
		t.Errorf("addMetric(max-1, 5) = %d, want %d", got, maxU32)
	}
}

// TestAdapterMetricSelectsTheFamily: the interface metric is per family, and the
// effective route metric must be summed with the matching one — a v6 route takes
// the v6 interface metric, a v4 route the v4. Picking the wrong family's number
// would miscalibrate the comparison the guard depends on.
func TestAdapterMetricSelectsTheFamily(t *testing.T) {
	a := adapter{metric4: 25, metric6: 5}
	if got := a.metric(winsys.AF_INET); got != 25 {
		t.Errorf("metric(AF_INET) = %d, want 25", got)
	}
	if got := a.metric(winsys.AF_INET6); got != 5 {
		t.Errorf("metric(AF_INET6) = %d, want 5", got)
	}
}
