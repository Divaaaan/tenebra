package windows

import "testing"

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
		if i.RouteMetric < 0 {
			t.Errorf("%s: negative metric %d", i.Name, i.RouteMetric)
		}
		if !i.HasDefaultRoute && i.RouteMetric != 0 {
			t.Errorf("%s: metric %d without a default route", i.Name, i.RouteMetric)
		}
		if i.Description != "" {
			described++
		}
	}
	if described == 0 {
		t.Error("no adapter descriptions at all: the GetAdaptersAddresses join did not land")
	}
}
