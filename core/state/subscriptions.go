package state

import (
	"encoding/json"
	"errors"
	"net/url"
	"time"

	"github.com/Divaaaan/tenebra/core/model"
	"github.com/Divaaaan/tenebra/core/nodeidentity"
	"github.com/Divaaaan/tenebra/core/profile"
	"github.com/Divaaaan/tenebra/core/redact"
)

type SubscriptionRecord struct {
	Profile     profile.Profile
	Revision    uint64
	HTTPAllowed bool
}

func SubscriptionAt(snapshot Snapshot, id string) (SubscriptionRecord, bool) {
	p, ok := snapshot.Profiles[id]
	if !ok || p.Source != profile.SourceSubscription || p.URL == "" {
		return SubscriptionRecord{}, false
	}
	copy, err := cloneProfile(p)
	if err != nil {
		return SubscriptionRecord{}, false
	}
	return SubscriptionRecord{copy, p.Revision, p.HTTPAllowed}, true
}

type SubscriptionUsage struct {
	Used, Total int64
	ExpiresAt   *time.Time
}
type SubscriptionMetadata struct {
	UpdatedAt time.Time
	Usage     *SubscriptionUsage
	Managed   bool
	Tier      string
}

type subscriptionIntent struct {
	Connection *ConnectionIntent
	Enabled    bool
	Binding    *Binding
}

func intentAt(s Snapshot, id string) subscriptionIntent {
	stamp := subscriptionIntent{Enabled: s.Multihop.Enabled}
	if s.Connection.ProfileID == id {
		value := s.Connection
		stamp.Connection = &value
	}
	for _, binding := range s.Multihop.Bindings {
		if binding.ProfileID == id {
			value := binding
			stamp.Binding = &value
			break
		}
	}
	return stamp
}

type PreparedSubscriptionRefresh struct {
	profileID, sourceURL string
	revision             uint64
	intent               subscriptionIntent
	candidate            profile.Profile
	required             []string
	binding              *Binding
	issues               []ResolutionIssue
}
type PreparedSubscriptionImport struct{ candidate profile.Profile }

func cloneProfile(p profile.Profile) (profile.Profile, error) {
	data, err := json.Marshal(p)
	if err != nil {
		return profile.Profile{}, ErrCorruptState
	}
	var result profile.Profile
	if json.Unmarshal(data, &result) != nil {
		return profile.Profile{}, ErrCorruptState
	}
	return result, nil
}
func applySubscriptionMetadata(p *profile.Profile, metadata SubscriptionMetadata) error {
	if metadata.UpdatedAt.IsZero() || (metadata.Usage != nil && (metadata.Usage.Used < 0 || metadata.Usage.Total < 0)) {
		return ErrCorruptState
	}
	p.UpdatedAt = metadata.UpdatedAt.UTC()
	p.Managed, p.Tier = metadata.Managed, metadata.Tier
	if metadata.Usage != nil {
		p.TrafficUsed, p.TrafficTotal = metadata.Usage.Used, metadata.Usage.Total
		p.ExpiresAt = nil
		if metadata.Usage.ExpiresAt != nil {
			expiry := *metadata.Usage.ExpiresAt
			p.ExpiresAt = &expiry
		}
	}
	return nil
}
func PrepareSubscriptionRefresh(snapshot Snapshot, id string, nodes []model.Node, metadata SubscriptionMetadata, extraRequired []string) (PreparedSubscriptionRefresh, error) {
	record, ok := SubscriptionAt(snapshot, id)
	if !ok {
		return PreparedSubscriptionRefresh{}, ErrProfileConflict
	}
	if err := snapshot.Validate(); err != nil {
		return PreparedSubscriptionRefresh{}, err
	}
	required, err := snapshot.RequiredNodeIDs(id)
	if err != nil {
		return PreparedSubscriptionRefresh{}, errors.Join(ErrRequiredReference, err)
	}
	seen := map[string]bool{}
	for _, value := range required {
		seen[value] = true
	}
	for _, value := range extraRequired {
		if _, _, err := profile.ResolveNode(record.Profile, value); err != nil {
			return PreparedSubscriptionRefresh{}, errors.Join(ErrRequiredReference, err)
		}
		if !seen[value] {
			required = append(required, value)
			seen[value] = true
		}
	}
	servers, err := profile.ReconcileServers(record.Profile.Servers, nodes, required)
	if err != nil {
		if errors.Is(err, profile.ErrRequiredNodeLost) {
			err = errors.Join(ErrRequiredReference, err)
		}
		return PreparedSubscriptionRefresh{}, err
	}
	candidate := record.Profile
	candidate.Servers = servers
	if err := applySubscriptionMetadata(&candidate, metadata); err != nil {
		return PreparedSubscriptionRefresh{}, err
	}
	if err := profile.ValidateForUse(candidate); err != nil {
		return PreparedSubscriptionRefresh{}, err
	}
	candidate, err = cloneProfile(candidate)
	if err != nil {
		return PreparedSubscriptionRefresh{}, err
	}
	proposal := PreparedSubscriptionRefresh{profileID: id, sourceURL: record.Profile.URL, revision: record.Revision, intent: intentAt(snapshot, id), candidate: candidate, required: append([]string(nil), required...)}
	// Disabled choices stay choices. A missing member acquires a prepared issue
	// ticket, rather than becoming an empty ID or silently selecting AUTO.
	if !snapshot.Multihop.Enabled && proposal.intent.Binding != nil {
		binding := *proposal.intent.Binding
		for _, item := range []struct {
			ref   *NodeRef
			field string
		}{{&binding.Entry, "multihop.entry"}, {&binding.Exit, "multihop.exit"}} {
			if item.ref.Resolution != Resolved {
				continue
			}
			if _, _, err := profile.ResolveNode(candidate, item.ref.NodeID); err == nil {
				continue
			}
			ticket, err := nodeidentity.NewID()
			if err != nil {
				return PreparedSubscriptionRefresh{}, err
			}
			*item.ref = NodeRef{ProfileID: id, Resolution: NeedsSelection, IssueID: ticket}
			proposal.issues = append(proposal.issues, ResolutionIssue{ID: ticket, Reason: "missing_node", Field: item.field, ProfileID: id})
		}
		proposal.binding = &binding
	}
	return proposal, nil
}
func ApplySubscriptionRefresh(snapshot *Snapshot, proposal PreparedSubscriptionRefresh) (profile.Profile, error) {
	if snapshot == nil || proposal.profileID == "" {
		return profile.Profile{}, ErrProfileConflict
	}
	current, exists := snapshot.Profiles[proposal.profileID]
	if !exists || current.Revision != proposal.revision || current.URL != proposal.sourceURL || current.Source != profile.SourceSubscription {
		return profile.Profile{}, ErrProfileConflict
	}
	if !equalJSON(intentAt(*snapshot, proposal.profileID), proposal.intent) {
		return profile.Profile{}, ErrIntentConflict
	}
	candidate, err := cloneProfile(proposal.candidate)
	if err != nil {
		return profile.Profile{}, err
	}
	for _, id := range proposal.required {
		if _, _, err := profile.ResolveNode(candidate, id); err != nil {
			return profile.Profile{}, errors.Join(ErrRequiredReference, err)
		}
	}
	if err := profile.ValidateTransition(current, candidate); err != nil {
		return profile.Profile{}, err
	}
	// Make every fallible check before editing the caller's candidate snapshot.
	snapshot.Profiles[proposal.profileID] = candidate
	if cached, ok := snapshot.LastGood[proposal.profileID]; ok && cached.Resolution == Resolved {
		if _, _, err := profile.ResolveNode(candidate, cached.NodeID); err != nil {
			delete(snapshot.LastGood, proposal.profileID)
		}
	}
	if proposal.binding != nil {
		for i, binding := range snapshot.Multihop.Bindings {
			if binding.ProfileID == proposal.profileID {
				snapshot.Multihop.Bindings[i] = *proposal.binding
			}
		}
	}
	for i := range snapshot.Issues {
		issue := &snapshot.Issues[i]
		kept := make([]NodeRef, 0, len(issue.Candidates))
		for _, ref := range issue.Candidates {
			if ref.ProfileID != proposal.profileID {
				kept = append(kept, ref)
				continue
			}
			if _, _, err := profile.ResolveNode(candidate, ref.NodeID); err == nil {
				kept = append(kept, ref)
			}
		}
		if len(kept) != len(issue.Candidates) {
			issue.Candidates = kept
		}
	}
	snapshot.Issues = append(snapshot.Issues, proposal.issues...)
	return cloneProfile(candidate)
}
func PrepareSubscriptionImport(snapshot Snapshot, name, sourceURL string, nodes []model.Node, metadata SubscriptionMetadata, allowHTTP bool) (PreparedSubscriptionImport, error) {
	if err := snapshot.Validate(); err != nil {
		return PreparedSubscriptionImport{}, err
	}
	parsed, err := url.Parse(sourceURL)
	if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "https" && !(parsed.Scheme == "http" && allowHTTP)) {
		return PreparedSubscriptionImport{}, ErrCorruptState
	}
	p, err := profile.NewProfile(redact.ProfileName(name, sourceURL), profile.SourceSubscription, sourceURL, nodes)
	if err != nil {
		return PreparedSubscriptionImport{}, err
	}
	p.HTTPAllowed = allowHTTP
	if err := applySubscriptionMetadata(&p, metadata); err != nil {
		return PreparedSubscriptionImport{}, err
	}
	if err := profile.ValidateForUse(p); err != nil {
		return PreparedSubscriptionImport{}, err
	}
	p, err = cloneProfile(p)
	if err != nil {
		return PreparedSubscriptionImport{}, err
	}
	return PreparedSubscriptionImport{p}, nil
}
func ApplySubscriptionImport(snapshot *Snapshot, proposal PreparedSubscriptionImport) (profile.Profile, error) {
	if snapshot == nil || proposal.candidate.ID == "" {
		return profile.Profile{}, ErrProfileConflict
	}
	if _, exists := snapshot.Profiles[proposal.candidate.ID]; exists {
		return profile.Profile{}, ErrProfileConflict
	}
	candidate, err := cloneProfile(proposal.candidate)
	if err != nil {
		return profile.Profile{}, err
	}
	if err := profile.ValidateForUse(candidate); err != nil {
		return profile.Profile{}, err
	}
	snapshot.Profiles[candidate.ID] = candidate
	snapshot.ProfileOrder = append(snapshot.ProfileOrder, candidate.ID)
	return cloneProfile(candidate)
}
