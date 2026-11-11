package state

import (
	"github.com/Divaaaan/tenebra/core/nodeidentity"
	"github.com/Divaaaan/tenebra/core/profile"
)

func (s Snapshot) resolved(ref NodeRef) bool {
	if ref.Resolution != Resolved || ref.IssueID != "" || !nodeidentity.ValidID(ref.NodeID) {
		return false
	}
	p, ok := s.Profiles[ref.ProfileID]
	if !ok {
		return false
	}
	for _, server := range p.Servers {
		if server.ID == ref.NodeID {
			return true
		}
	}
	return false
}

func (s Snapshot) BindingFor(profileID string) (Binding, error) {
	for _, binding := range s.Multihop.Bindings {
		if binding.ProfileID == profileID {
			if s.resolved(binding.Entry) && s.resolved(binding.Exit) && binding.Entry.ProfileID == profileID && binding.Exit.ProfileID == profileID && binding.Entry.NodeID != binding.Exit.NodeID {
				return binding, nil
			}
			return Binding{}, ErrNeedsSelection
		}
	}
	return Binding{}, ErrNeedsSelection
}

func (s Snapshot) RequiredNodeIDs(profileID string) ([]string, error) {
	var result []string
	seen := map[string]bool{}
	add := func(ref NodeRef) error {
		if !s.resolved(ref) {
			return ErrNeedsSelection
		}
		if !seen[ref.NodeID] {
			seen[ref.NodeID] = true
			result = append(result, ref.NodeID)
		}
		return nil
	}
	if s.Connection.ProfileID == profileID && s.Connection.Selection.Kind == Pinned {
		if err := add(s.Connection.Selection.Ref); err != nil {
			return nil, err
		}
	}
	if s.Multihop.Enabled {
		binding, err := s.BindingFor(profileID)
		if err != nil {
			return nil, err
		}
		add(binding.Entry)
		add(binding.Exit)
	}
	return result, nil
}

func (s Snapshot) Validate() error {
	if s.SchemaVersion != SchemaVersion || s.IdentityVersion != 2 || s.MatcherVersion != 1 || s.Preferences.Version != PreferencesVersion {
		return ErrUnsupportedSchema
	}
	if s.MigrationID == "" || s.Revision == 0 || s.Profiles == nil || s.LastGood == nil || len(s.ProfileOrder) != len(s.Profiles) {
		return ErrCorruptState
	}
	profiles, nodes := map[string]bool{}, map[string]bool{}
	for _, id := range s.ProfileOrder {
		p, ok := s.Profiles[id]
		if !ok || id == "" || p.ID != id || profiles[id] || p.Revision == 0 {
			return ErrCorruptState
		}
		profiles[id] = true
		if err := profile.ValidateIdentity(p); err != nil {
			return err
		}
		for _, server := range p.Servers {
			if nodes[server.ID] {
				return profile.ErrDuplicateNodeID
			}
			nodes[server.ID] = true
		}
	}
	issues := map[string]bool{}
	for _, issue := range s.Issues {
		if issue.ID == "" || issues[issue.ID] || issue.Reason == "" || issue.Field == "" {
			return ErrCorruptState
		}
		issues[issue.ID] = true
		for _, candidate := range issue.Candidates {
			if !s.resolved(candidate) {
				return ErrCorruptState
			}
		}
	}
	check := func(ref NodeRef) error {
		switch ref.Resolution {
		case Resolved:
			if !s.resolved(ref) {
				return ErrCorruptState
			}
		case NeedsSelection:
			if ref.NodeID != "" || ref.IssueID == "" || !issues[ref.IssueID] {
				return ErrCorruptState
			}
		default:
			return ErrCorruptState
		}
		return nil
	}
	if s.Connection.Selection.Kind == Pinned {
		if s.Connection.Selection.Ref.ProfileID != s.Connection.ProfileID {
			return ErrCorruptState
		}
		if err := check(s.Connection.Selection.Ref); err != nil {
			return err
		}
	} else if s.Connection.Selection.Kind != Auto {
		return ErrCorruptState
	} else if ref := s.Connection.Selection.Ref; ref != (NodeRef{}) {
		if ref.Resolution != NeedsSelection || ref.ProfileID != s.Connection.ProfileID {
			return ErrCorruptState
		}
		if err := check(ref); err != nil {
			return err
		}
	}
	if s.Connection.ProfileID != "" {
		if _, exists := s.Profiles[s.Connection.ProfileID]; !exists && s.Connection.Selection.Ref.Resolution != NeedsSelection {
			return ErrCorruptState
		}
	}
	bindings := map[string]bool{}
	for _, binding := range s.Multihop.Bindings {
		if binding.ProfileID == "" || bindings[binding.ProfileID] || binding.Entry.ProfileID != binding.ProfileID || binding.Exit.ProfileID != binding.ProfileID {
			return ErrCorruptState
		}
		bindings[binding.ProfileID] = true
		if err := check(binding.Entry); err != nil {
			return err
		}
		if err := check(binding.Exit); err != nil {
			return err
		}
		if binding.Entry.Resolution == Resolved && binding.Exit.Resolution == Resolved && binding.Entry.NodeID == binding.Exit.NodeID {
			return ErrCorruptState
		}
	}
	for profileID, ref := range s.LastGood {
		if profileID != ref.ProfileID {
			return ErrCorruptState
		}
		if err := check(ref); err != nil {
			return err
		}
	}
	return nil
}
