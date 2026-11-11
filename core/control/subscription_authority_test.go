package control

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"testing"

	"github.com/Divaaaan/tenebra/core/maintenance"
)

type admissionEndpoint maintenance.RunID

func (e admissionEndpoint) RunID() maintenance.RunID { return maintenance.RunID(e) }
func (e admissionEndpoint) DialNumeric(context.Context, string, netip.AddrPort, func(net.Conn) (net.Conn, error)) (net.Conn, error) {
	panic("pure classifier must not dial")
}

func TestSubscriptionAdmissionSlotsBoundNonterminalWork(t *testing.T) {
	var slots subscriptionSlots
	for index := range 32 {
		if err := slots.reserve(fmt.Sprintf("operation-%d", index), "session-a", fmt.Sprintf("profile-%d", index), true); err != nil {
			t.Fatal(err)
		}
	}
	if err := slots.reserve("overflow", "session-a", "new-profile", true); !errors.Is(err, errSubscriptionCapacity) {
		t.Fatal(err)
	}
	if err := slots.reserve("duplicate-resource", "session-b", "profile-0", true); !errors.Is(err, errSubscriptionBusy) {
		t.Fatal(err)
	}
	slots.release("operation-0")
	if err := slots.reserve("replacement", "session-a", "profile-0", true); err != nil {
		t.Fatal(err)
	}
	for index := range 32 {
		slots.release(fmt.Sprintf("operation-%d", index))
	}
	slots.release("replacement")
	slots.release("replacement")
	if len(slots.operations) != 0 || len(slots.resources) != 0 || len(slots.sessions) != 0 {
		t.Fatal("terminal resources leaked")
	}
}

func TestSubscriptionAdmissionMatrix(t *testing.T) {
	accepted := subscriptionAcceptedInfo{Connected: true, Generation: 1, EngineRunID: "run-a", ProfileID: "profile-a",
		NodeID: "node-a", RequiredNodeIDs: []string{"node-a"}, DesiredConfigRevision: 5, AppliedConfigRevision: 5,
		Endpoint: admissionEndpoint("run-a"), Protected: true}
	changed := func(update func(*subscriptionAcceptedInfo)) subscriptionAcceptedInfo {
		copy := accepted
		update(&copy)
		return copy
	}
	tests := []struct {
		name      string
		info      subscriptionAcceptedInfo
		manual    bool
		egress    maintenance.Egress
		admission subscriptionAdmission
	}{
		{"accepted VPN", accepted, false, maintenance.VPNOnly, subscriptionAdmit},
		{"manual accepted VPN", accepted, true, maintenance.VPNOnly, subscriptionAdmit},
		{"pending desired", changed(func(i *subscriptionAcceptedInfo) { i.Pending = true }), true, "", subscriptionDefer},
		{"revision mismatch", changed(func(i *subscriptionAcceptedInfo) { i.DesiredConfigRevision++ }), false, "", subscriptionDefer},
		{"no transport background", changed(func(i *subscriptionAcceptedInfo) { i.Endpoint = nil }), false, "", subscriptionDefer},
		{"no transport manual", changed(func(i *subscriptionAcceptedInfo) { i.Endpoint = nil }), true, "", subscriptionUnsupported},
		{"different engine run", changed(func(i *subscriptionAcceptedInfo) { i.Endpoint = admissionEndpoint("run-b") }), true, "", subscriptionUnsupported},
		{"no accepted generation", changed(func(i *subscriptionAcceptedInfo) { i.Generation = 0 }), true, "", subscriptionDefer},
		{"legacy accepted", changed(func(i *subscriptionAcceptedInfo) { i.Protected = false; i.ExplicitLegacy = true }), false, maintenance.VPNOnly, subscriptionAdmit},
		{"protected idle background", subscriptionAcceptedInfo{Protected: true}, false, "", subscriptionDefer},
		{"protected idle manual", subscriptionAcceptedInfo{Protected: true}, true, maintenance.DirectOnce, subscriptionChallenge},
		{"confirmed unprotected background", subscriptionAcceptedInfo{ConfirmedUnprotected: true}, false, maintenance.UnprotectedDirect, subscriptionAdmit},
		{"confirmed unprotected manual", subscriptionAcceptedInfo{ConfirmedUnprotected: true}, true, maintenance.UnprotectedDirect, subscriptionAdmit},
		{"unknown background", subscriptionAcceptedInfo{}, false, "", subscriptionDefer},
		{"unknown manual", subscriptionAcceptedInfo{}, true, maintenance.DirectOnce, subscriptionChallenge},
		{"pending idle cannot use stale OFF", subscriptionAcceptedInfo{Pending: true, ConfirmedUnprotected: true}, true, "", subscriptionDefer},
		{"protection wins over stale OFF", subscriptionAcceptedInfo{Protected: true, ConfirmedUnprotected: true}, true, maintenance.DirectOnce, subscriptionChallenge},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			egress, admission := classifySubscriptionEgress(test.info, test.manual)
			if egress != test.egress || admission != test.admission {
				t.Fatalf("got %s/%s, want %s/%s", egress, admission, test.egress, test.admission)
			}
		})
	}
}
