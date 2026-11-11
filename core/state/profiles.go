package state

import (
	"context"

	"github.com/Divaaaan/tenebra/core/model"
	"github.com/Divaaaan/tenebra/core/profile"
)

type ProfileView struct{ store *Store }

func (s *Store) Profiles() *ProfileView { return &ProfileView{store: s} }
func (v *ProfileView) Dir() string      { return v.store.directory }
func (v *ProfileView) List() []profile.Profile {
	snapshot := v.store.View()
	result := make([]profile.Profile, 0, len(snapshot.ProfileOrder))
	for _, id := range snapshot.ProfileOrder {
		result = append(result, snapshot.Profiles[id])
	}
	return result
}
func (v *ProfileView) Get(id string) (profile.Profile, bool) {
	snapshot := v.store.View()
	p, exists := snapshot.Profiles[id]
	return p, exists
}
func (v *ProfileView) Add(p profile.Profile) error {
	if err := profile.ValidateIdentity(p); err != nil {
		return err
	}
	nodes := make([]model.Node, len(p.Servers))
	for i, server := range p.Servers {
		nodes[i] = server.Node
	}
	if err := model.ValidateNodeRecords(nodes); err != nil {
		return err
	}
	_, err := v.store.Transact(context.Background(), AnyRevision, func(candidate *Snapshot) error {
		if _, exists := candidate.Profiles[p.ID]; exists {
			return profile.ErrExists
		}
		if p.ID == "" {
			return ErrCorruptState
		}
		candidate.Profiles[p.ID] = p
		candidate.ProfileOrder = append(candidate.ProfileOrder, p.ID)
		return nil
	})
	return err
}
func (v *ProfileView) Update(p profile.Profile) error {
	_, err := v.store.Transact(context.Background(), AnyRevision, func(candidate *Snapshot) error {
		current, exists := candidate.Profiles[p.ID]
		if !exists {
			return profile.ErrNotFound
		}
		if p.Revision != current.Revision {
			return ErrProfileConflict
		}
		candidate.Profiles[p.ID] = p
		return nil
	})
	return err
}
func (v *ProfileView) Remove(id string) error {
	_, err := v.store.Transact(context.Background(), AnyRevision, func(candidate *Snapshot) error {
		if _, exists := candidate.Profiles[id]; !exists {
			return profile.ErrNotFound
		}
		delete(candidate.Profiles, id)
		delete(candidate.LastGood, id)
		order := candidate.ProfileOrder[:0]
		for _, item := range candidate.ProfileOrder {
			if item != id {
				order = append(order, item)
			}
		}
		candidate.ProfileOrder = order
		if candidate.Connection.ProfileID == id {
			candidate.Connection.ProfileID = ""
			candidate.Connection.Selection = Selection{Kind: Auto}
		}
		bindings := candidate.Multihop.Bindings[:0]
		for _, binding := range candidate.Multihop.Bindings {
			if binding.ProfileID != id {
				bindings = append(bindings, binding)
			}
		}
		candidate.Multihop.Bindings = bindings
		issues := candidate.Issues[:0]
		for _, issue := range candidate.Issues {
			if issue.ProfileID != id {
				issues = append(issues, issue)
			}
		}
		candidate.Issues = issues
		return nil
	})
	return err
}
