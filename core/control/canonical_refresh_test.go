package control

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"testing"

	"github.com/Divaaaan/tenebra/core/state"
	"github.com/Divaaaan/tenebra/core/subscription"
)

func TestCanonicalTargetCASIgnoresUnrelatedGlobalRevision(t *testing.T) {
	for _, change := range []string{"unrelated", "target_changed", "removed", "required_pin"} {
		t.Run(change, func(t *testing.T) {
			d, store, _ := newCanonicalTestDaemon(t)
			p := addCanonicalProfile(t, store)
			// This CAS fixture explicitly permits unprotected maintenance. The
			// production default remains deferred until lifecycle acceptance.
			d.subscriptionOps.accepted = subscriptionAcceptedInfo{ConfirmedUnprotected: true}
			d.subscriptionOps.fetch = func(ctx context.Context, url string, _ subscription.FetchOptions) ([]byte, http.Header, error) {
				return d.fetch(ctx, url)
			}
			t.Cleanup(d.subscriptionOps.close)
			var afterConcurrent state.Snapshot
			d.fetch = func(context.Context, string) ([]byte, http.Header, error) {
				_, err := d.CommitCanonical(context.Background(), state.AnyRevision, func(s *state.Snapshot) error {
					s.Preferences.AdBlock = true
					switch change {
					case "target_changed":
						target := s.Profiles[p.ID]
						target.Tier = "premium"
						s.Profiles[p.ID] = target
					case "removed":
						delete(s.Profiles, p.ID)
						s.ProfileOrder = nil
					case "required_pin":
						s.Connection = state.ConnectionIntent{ProfileID: p.ID, Selection: state.Selection{Kind: state.Pinned, Ref: state.NodeRef{ProfileID: p.ID, NodeID: p.Servers[0].ID, Resolution: state.Resolved}}}
					}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
				afterConcurrent = store.View()
				return []byte("trojan://SYNTHETIC_ONLY@node.test:443?security=tls&sni=changed.test#display"), http.Header{"Subscription-Userinfo": []string{"upload=7; download=3; total=100"}}, nil
			}
			updated, _, err := d.refreshProfile(context.Background(), p)
			if change == "unrelated" {
				if err != nil || !store.View().Preferences.AdBlock || updated.TrafficUsed != 10 || updated.TrafficTotal != 100 || updated.Revision != p.Revision+1 {
					t.Fatal("unrelated preference blocked/was reverted by refresh", err)
				}
			} else {
				if err == nil {
					t.Fatal("stale proposal committed")
				}
				if change == "required_pin" && !errors.Is(err, state.ErrIntentConflict) {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(store.View(), afterConcurrent) {
					t.Fatal("failed proposal changed nodes/quota/tier/time/revision after concurrent edit")
				}
			}
		})
	}
}
