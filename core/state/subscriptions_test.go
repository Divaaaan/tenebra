package state

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Divaaaan/tenebra/core/model"
	"github.com/Divaaaan/tenebra/core/profile"
)

func subscriptionFixture(t *testing.T) Snapshot {
	t.Helper()
	s := canonicalFixture(t)
	p := s.Profiles["p"]
	p.Source, p.URL = profile.SourceSubscription, "https://provider.test/sub/private"
	p.Tier = "premium"
	p.TrafficTotal = 100
	s.Profiles[p.ID] = p
	return s
}
func metadataFixture() SubscriptionMetadata {
	return SubscriptionMetadata{UpdatedAt: time.Date(2026, 11, 9, 1, 2, 3, 0, time.UTC), Managed: true, Tier: "free"}
}
func nodesOf(p profile.Profile) []model.Node {
	copy, err := cloneProfile(p)
	if err != nil {
		panic(err)
	}
	p = copy
	result := make([]model.Node, len(p.Servers))
	for i, server := range p.Servers {
		result[i] = server.Node
	}
	return result
}

func TestCanonicalRefreshProfileCASIgnoresUnrelatedChanges(t *testing.T) {
	s := subscriptionFixture(t)
	p := s.Profiles["p"]
	proposal, err := PrepareSubscriptionRefresh(s, p.ID, nodesOf(p), metadataFixture(), nil)
	if err != nil {
		t.Fatal(err)
	}
	s.Preferences.AdBlock = true
	s.Revision += 10
	got, err := ApplySubscriptionRefresh(&s, proposal)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Preferences.AdBlock || s.Revision != 11 || got.Revision != p.Revision || got.TrafficTotal != 100 || got.Tier != "free" || got.Servers[0].ID != p.Servers[0].ID {
		t.Fatal("proposal assigned revisions, lost quota, rebound identity or reverted unrelated fields")
	}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []string{"revision", "url", "remove"} {
		t.Run(change, func(t *testing.T) {
			latest := subscriptionFixture(t)
			target := latest.Profiles[p.ID]
			switch change {
			case "revision":
				target.Revision++
				latest.Profiles[p.ID] = target
			case "url":
				target.URL += "/new"
				latest.Profiles[p.ID] = target
			case "remove":
				delete(latest.Profiles, p.ID)
			}
			before, _ := cloneSnapshot(latest)
			if _, err := ApplySubscriptionRefresh(&latest, proposal); !errors.Is(err, ErrProfileConflict) {
				t.Fatal(err)
			}
			if !equalJSON(before, latest) {
				t.Fatal("failed proposal edited latest state")
			}
		})
	}
}
func TestCanonicalRefreshRejectsIntentAndRequiredNodeLoss(t *testing.T) {
	s := subscriptionFixture(t)
	p := s.Profiles["p"]
	s.Connection = ConnectionIntent{ProfileID: p.ID, Selection: Selection{Kind: Pinned, Ref: NodeRef{ProfileID: p.ID, NodeID: p.Servers[0].ID, Resolution: Resolved}}}
	changed := nodesOf(p)
	changed[0].TLS.ServerName = "changed-sni.test"
	before, _ := cloneSnapshot(s)
	if _, err := PrepareSubscriptionRefresh(s, p.ID, changed, metadataFixture(), nil); !errors.Is(err, ErrRequiredReference) {
		t.Fatal("required pin rebound", err)
	}
	if !equalJSON(s, before) {
		t.Fatal("preparation modified snapshot")
	}
	proposal, err := PrepareSubscriptionRefresh(s, p.ID, nodesOf(p), metadataFixture(), nil)
	if err != nil {
		t.Fatal(err)
	}
	s.Connection.Selection = Selection{Kind: Auto}
	before, _ = cloneSnapshot(s)
	if _, err := ApplySubscriptionRefresh(&s, proposal); !errors.Is(err, ErrIntentConflict) {
		t.Fatal(err)
	}
	if !equalJSON(s, before) {
		t.Fatal("intent refusal edited state")
	}
}
func TestCanonicalSubscriptionProposalHasNoMutableAliases(t *testing.T) {
	s := subscriptionFixture(t)
	p := s.Profiles["p"]
	nodes := nodesOf(p)
	nodes[0].TLS.AdditionalFields = map[string]json.RawMessage{"future": json.RawMessage(`{"value":1}`)}
	expiry := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	meta := metadataFixture()
	meta.Usage = &SubscriptionUsage{Used: 7, Total: 17, ExpiresAt: &expiry}
	proposal, err := PrepareSubscriptionRefresh(s, p.ID, nodes, meta, nil)
	if err != nil {
		t.Fatal(err)
	}
	nodes[0].TLS.ServerName = "mutated-after-prepare.test"
	nodes[0].TLS.AdditionalFields["future"][9] = '9'
	expiry = time.Time{}
	meta.Usage.Used = 999
	p.Name = "mutated-source"
	s.Profiles[p.ID] = p
	got, err := ApplySubscriptionRefresh(&s, proposal)
	if err != nil {
		t.Fatal(err)
	}
	if got.Servers[0].TLS.ServerName == nodes[0].TLS.ServerName || string(got.Servers[0].TLS.AdditionalFields["future"]) != `{"value":1}` || got.ExpiresAt.IsZero() || got.TrafficUsed != 7 {
		t.Fatal("proposal retained caller-owned aliases")
	}
	got.Servers[0].TLS.AdditionalFields["future"][9] = '8'
	got.TrafficUsed = 555
	if s.Profiles[p.ID].TrafficUsed != 7 || string(s.Profiles[p.ID].Servers[0].TLS.AdditionalFields["future"]) != `{"value":1}` {
		t.Fatal("Apply result aliases saved candidate")
	}
}
func TestCanonicalRefreshLostDisabledChoiceRemainsUnresolved(t *testing.T) {
	s := subscriptionFixture(t)
	p := s.Profiles["p"]
	other := legacyNode("n2_ffffffffffffffffffffffffffffffff", "other.test")
	p.Servers = append(p.Servers, other)
	s.Profiles[p.ID] = p
	s.Multihop = MultihopRequirement{Enabled: false, Bindings: []Binding{{ProfileID: p.ID, Entry: NodeRef{ProfileID: p.ID, NodeID: p.Servers[0].ID, Resolution: Resolved}, Exit: NodeRef{ProfileID: p.ID, NodeID: other.ID, Resolution: Resolved}}}}
	s.LastGood[p.ID] = NodeRef{ProfileID: p.ID, NodeID: other.ID, Resolution: Resolved}
	proposal, err := PrepareSubscriptionRefresh(s, p.ID, nodesOf(p)[:1], metadataFixture(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ApplySubscriptionRefresh(&s, proposal); err != nil {
		t.Fatal(err)
	}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	if s.Multihop.Enabled || s.Multihop.Bindings[0].Exit.Resolution != NeedsSelection || s.Multihop.Bindings[0].Exit.NodeID != "" || len(s.Issues) != 1 {
		t.Fatal("lost disabled choice became an empty resolved ref or AUTO")
	}
	if _, ok := s.LastGood[p.ID]; ok {
		t.Fatal("lost measurement cache retained dangling ID")
	}
}
func TestCanonicalImportCommitAssignsRevisionsAndSanitizesAlias(t *testing.T) {
	store, files := openFakeCanonical(t)
	if err := store.Admit(); err != nil {
		t.Fatal(err)
	}
	s := store.View()
	p := s.Profiles["p"]
	proposal, err := PrepareSubscriptionImport(s, "https://provider.test/private?token=canary", "https://provider.test/private?token=canary", nodesOf(p), metadataFixture(), false)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := store.Transact(context.Background(), AnyRevision, func(s *Snapshot) error { _, err := ApplySubscriptionImport(s, proposal); return err })
	if err != nil {
		t.Fatal(err)
	}
	added := accepted.Profiles[accepted.ProfileOrder[1]]
	if added.Revision != 1 || accepted.Revision != s.Revision+1 || added.URL != "https://provider.test/private?token=canary" || added.Name == added.URL || files.writes != 1 {
		t.Fatal("import publication or display alias incorrect")
	}
	if _, err := store.Transact(context.Background(), AnyRevision, func(s *Snapshot) error { _, err := ApplySubscriptionImport(s, proposal); return err }); !errors.Is(err, ErrProfileConflict) {
		t.Fatal("replayed import added twice", err)
	}
	if _, err := PrepareSubscriptionImport(s, "x", "http://provider.test/sub", nodesOf(p), metadataFixture(), false); err == nil {
		t.Fatal("HTTP permission implicit")
	}
}
