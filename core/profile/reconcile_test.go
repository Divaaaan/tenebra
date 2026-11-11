package profile

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/Divaaaan/tenebra/core/model"
)

const opaqueA = "n2_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const opaqueB = "n2_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
const opaqueC = "n2_cccccccccccccccccccccccccccccccc"

func TestIdentityReconcilePrioritizesRequiredIDs(t *testing.T) {
	node := sampleNodes()[0]
	previous := []Server{{ID: opaqueA, Node: node}, {ID: opaqueB, Node: node}}
	got, err := ReconcileServers(previous, []model.Node{node, node}, []string{opaqueB})
	if err != nil || len(got) != 1 || got[0].ID != opaqueB {
		t.Fatalf("required second duplicate lost: %+v %v", got, err)
	}
	if got, err := ReconcileServers(previous, []model.Node{node, node}, []string{opaqueA, opaqueB}); !errors.Is(err, ErrRequiredNodeLost) || got != nil {
		t.Fatalf("two required IDs rebound onto one row: %+v %v", got, err)
	}
}

func TestIdentityReconcileRenameReorderAndSNIPin(t *testing.T) {
	a := sampleNodes()[0]
	a.TLS = &model.TLS{Enabled: true, ServerName: "a.test"}
	b := a
	b.TLS = &model.TLS{Enabled: true, ServerName: "b.test"}
	previous := []Server{{ID: opaqueA, Node: a}, {ID: opaqueB, Node: b}}
	a.Name = "renamed a"
	b.Name = "renamed b"
	got, err := ReconcileServers(previous, []model.Node{b, a}, []string{opaqueA, opaqueB})
	if err != nil || len(got) != 2 || got[0].ID != opaqueB || got[1].ID != opaqueA {
		t.Fatalf("SNI identity swapped on rename/reorder: %+v %v", got, err)
	}
	changed := b
	changed.TLS = &model.TLS{Enabled: true, ServerName: "changed.test"}
	if got, err := ReconcileServers(previous, []model.Node{changed, a}, []string{opaqueB}); !errors.Is(err, ErrRequiredNodeLost) || got != nil {
		t.Fatal("required old SNI silently replaced")
	}
	got, err = reconcileServers(previous, []model.Node{changed, a}, nil, func() (string, error) { return opaqueC, nil })
	if err != nil || got[0].ID != opaqueC || got[1].ID != opaqueA {
		t.Fatalf("changed spec did not get new identity: %+v %v", got, err)
	}
}

func TestIdentityCandidateRNGFailureAndCollision(t *testing.T) {
	previous := []Server{{ID: opaqueA, Node: sampleNodes()[0]}}
	before, _ := json.Marshal(previous)
	failure := errors.New("synthetic RNG failure")
	for _, test := range []struct {
		name, id string
		err      error
	}{{"rng", "", failure}, {"duplicate_new", opaqueB, nil}, {"disappearing_previous", opaqueA, nil}} {
		t.Run(test.name, func(t *testing.T) {
			got, err := reconcileServers(previous, sampleNodes()[1:], nil, func() (string, error) { return test.id, test.err })
			if err == nil || got != nil {
				t.Fatal("incomplete identity allocation was published")
			}
			after, _ := json.Marshal(previous)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("failed proposal mutated previous profile")
			}
		})
	}
}

func TestIdentityResolverNeverUsesLastDuplicateID(t *testing.T) {
	a := sampleNodes()[0]
	a.TLS = &model.TLS{Enabled: true, ServerName: "a.test"}
	b := a
	b.TLS = &model.TLS{Enabled: true, ServerName: "b.test"}
	p := Profile{IdentityVersion: 2, Servers: []Server{{ID: opaqueA, Node: a}, {ID: opaqueB, Node: b}}}
	server, tagA, err := ResolveNode(p, opaqueA)
	if err != nil || server.TLS.ServerName != "a.test" || tagA == "" {
		t.Fatal("first actual selector member unresolved")
	}
	server, tagB, err := ResolveNode(p, opaqueB)
	if err != nil || server.TLS.ServerName != "b.test" || tagB == "" || tagA == tagB {
		t.Fatal("SNI variant resolved to wrong actual tag")
	}
	if _, _, err := ResolveNode(p, opaqueC); !errors.Is(err, ErrNodeNotFound) {
		t.Fatal("missing explicit ref selected fallback")
	}
	p.Servers[1].ID = opaqueA
	if _, _, err := ResolveNode(p, opaqueA); !errors.Is(err, ErrDuplicateNodeID) {
		t.Fatal("duplicate selected first/last row")
	}
	p.Servers[1].ID = opaqueB
	p.Servers[1].Protocol = "unsupported"
	if _, _, err := ResolveNode(p, opaqueB); !errors.Is(err, ErrNodeUnusable) {
		t.Fatal("unsupported explicit ref selected another member")
	}
}

func TestIdentityTransitionRejectsConnectionRebind(t *testing.T) {
	p := Profile{IdentityVersion: 2, Servers: []Server{{ID: opaqueA, Node: sampleNodes()[0]}}}
	candidate := p
	candidate.Servers = append([]Server(nil), p.Servers...)
	candidate.Servers[0].Name = "renamed"
	if err := ValidateTransition(p, candidate); err != nil {
		t.Fatal(err)
	}
	candidate.Servers[0].TLS = &model.TLS{Enabled: true, ServerName: "new.test"}
	if err := ValidateTransition(p, candidate); !errors.Is(err, ErrIdentityRebind) {
		t.Fatalf("same ID/new connection accepted: %v", err)
	}
}
