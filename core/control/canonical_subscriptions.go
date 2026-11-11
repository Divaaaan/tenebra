package control

import (
	"context"

	"github.com/Divaaaan/tenebra/core/profile"
)

func (d *Daemon) refreshCanonicalProfile(ctx context.Context, target profile.Profile) (profile.Profile, bool, error) {
	if d.canonical.Ready() != nil {
		return profile.Profile{}, false, subscriptionError("storage_error")
	}
	owner := d.subscriptionOps
	owner.mu.Lock()
	_, admission := classifySubscriptionEgress(owner.accepted, false)
	owner.mu.Unlock()
	if admission != subscriptionAdmit {
		return profile.Profile{}, false, subscriptionError("waiting_for_vpn")
	}
	// This owner belongs to the background/preflight caller, never to a GUI.
	// EOF on an unrelated control stream cannot cancel it or supply consent.
	owned, session := d.newOwnedSubscriptionSession(ctx, false)
	defer session.cancel()
	defer owner.endSession(session)
	updated, err := d.runSubscriptionToProfile(owned, SubscriptionStart{Kind: "refresh", ProfileID: target.ID}, &target)
	if err != nil {
		return profile.Profile{}, false, err
	}
	return updated, profileChanged(target, updated), nil
}
