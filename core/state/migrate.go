package state

import (
	"bytes"
	"encoding/json"
	"reflect"
	"sort"

	"github.com/Divaaaan/tenebra/core/model"
	"github.com/Divaaaan/tenebra/core/nodeidentity"
	"github.com/Divaaaan/tenebra/core/profile"
	"github.com/Divaaaan/tenebra/core/redact"
)

// Migration is pure: the repository takes the backup and commits the result.
func MigrateLegacy(bundle LegacyBundle, nextID func() (string, error)) (Snapshot, error) {
	if nextID == nil {
		return Snapshot{}, ErrCorruptState
	}
	for _, file := range []LegacyFile{bundle.Profiles, bundle.Settings, bundle.LastGood} {
		if !file.Present && len(file.Bytes) > 0 {
			return Snapshot{}, ErrCorruptState
		}
	}
	var profiles []profile.Profile
	if bundle.Profiles.Present {
		data := bytes.TrimSpace(bundle.Profiles.Bytes)
		if len(data) == 0 || data[0] != '[' || checkJSON(data) != nil || json.Unmarshal(data, &profiles) != nil {
			return Snapshot{}, ErrCorruptState
		}
		if err := validateShape(data, reflect.TypeOf([]profile.Profile{}), 0); err != nil {
			return Snapshot{}, err
		}
	}
	preferences, intent, multihop, err := migratePreferences(bundle.Settings)
	if err != nil {
		return Snapshot{}, err
	}
	seenIDs := map[string]bool{}
	allocate := func(prefix string) (string, error) {
		id, err := nextID()
		if err != nil {
			return "", err
		}
		if !nodeidentity.ValidID(id) {
			return "", profile.ErrInvalidNodeID
		}
		if seenIDs[id] {
			return "", profile.ErrDuplicateNodeID
		}
		seenIDs[id] = true
		return prefix + id[3:], nil
	}
	migrationID, err := allocate("m2_")
	if err != nil {
		return Snapshot{}, err
	}
	snapshot := Snapshot{SchemaVersion: SchemaVersion, IdentityVersion: 2, MatcherVersion: 1, MigrationID: migrationID, Revision: 1,
		Profiles: map[string]profile.Profile{}, ProfileOrder: []string{}, Preferences: preferences,
		Connection: ConnectionIntent{Autoconnect: intent.Autoconnect, ProfileID: intent.LastProfile, Selection: Selection{Kind: Auto}},
		Multihop:   MultihopRequirement{Enabled: multihop.Enabled, Bindings: []Binding{}}, LastGood: map[string]NodeRef{}, Issues: []ResolutionIssue{}}
	storedIDs := map[string]map[string][]int{}
	for _, p := range profiles {
		if p.ID == "" {
			return Snapshot{}, ErrCorruptState
		}
		if _, exists := snapshot.Profiles[p.ID]; exists {
			return Snapshot{}, ErrCorruptState
		}
		if p.IdentityVersion < 0 || p.IdentityVersion > 2 {
			return Snapshot{}, ErrUnsupportedSchema
		}
		byID := map[string][]int{}
		for index, server := range p.Servers {
			byID[server.ID] = append(byID[server.ID], index)
			id, err := allocate("n2_")
			if err != nil {
				return Snapshot{}, err
			}
			p.Servers[index].ID = id
		}
		p.Name = redact.ProfileName(p.Name, p.URL)
		p.IdentityVersion = 2
		p.Revision = 1
		snapshot.Profiles[p.ID] = p
		snapshot.ProfileOrder = append(snapshot.ProfileOrder, p.ID)
		storedIDs[p.ID] = byID
	}
	newIssue := func(reason, field, profileID string, candidates []NodeRef) (NodeRef, error) {
		id, err := allocate("issue_")
		if err != nil {
			return NodeRef{}, err
		}
		snapshot.Issues = append(snapshot.Issues, ResolutionIssue{ID: id, Reason: reason, Field: field, ProfileID: profileID, Candidates: candidates})
		return NodeRef{ProfileID: profileID, Resolution: NeedsSelection, IssueID: id}, nil
	}
	resolve := func(profileID, oldID, field string) (NodeRef, error) {
		p, exists := snapshot.Profiles[profileID]
		if !exists {
			return newIssue("missing_profile", field, profileID, nil)
		}
		indices := storedIDs[profileID][oldID]
		if oldID == "" || len(indices) == 0 {
			return newIssue("missing_node", field, profileID, nil)
		}
		first := p.Servers[indices[0]]
		matcher, err := nodeidentity.NewMatcher([]model.Node{first.Node})
		if err != nil {
			return NodeRef{}, err
		}
		var candidates []NodeRef
		ambiguous := false
		for _, index := range indices {
			server := p.Servers[index]
			matches, err := matcher.Candidates(server.Node)
			if err != nil {
				return NodeRef{}, err
			}
			ambiguous = ambiguous || len(matches) != 1
			candidates = append(candidates, NodeRef{ProfileID: profileID, NodeID: server.ID, Resolution: Resolved})
		}
		if ambiguous {
			return newIssue("ambiguous_legacy_reference", field, profileID, candidates)
		}
		return NodeRef{ProfileID: profileID, NodeID: first.ID, Resolution: Resolved}, nil
	}
	if intent.LastNode != "" {
		ref, err := resolve(intent.LastProfile, intent.LastNode, "connection.selection")
		if err != nil {
			return Snapshot{}, err
		}
		snapshot.Connection.Selection = Selection{Kind: Pinned, Ref: ref}
	} else if intent.LastProfile != "" {
		if _, exists := snapshot.Profiles[intent.LastProfile]; !exists {
			ref, err := newIssue("missing_profile", "connection.profileId", intent.LastProfile, nil)
			if err != nil {
				return Snapshot{}, err
			}
			snapshot.Connection.Selection.Ref = ref
		}
	}
	if multihop.Enabled || multihop.EntryID != "" || multihop.ExitID != "" {
		for _, profileID := range snapshot.ProfileOrder {
			entry, err := resolve(profileID, multihop.EntryID, "multihop.entry")
			if err != nil {
				return Snapshot{}, err
			}
			exit, err := resolve(profileID, multihop.ExitID, "multihop.exit")
			if err != nil {
				return Snapshot{}, err
			}
			if entry.Resolution == Resolved && exit.Resolution == Resolved && entry.NodeID == exit.NodeID {
				exit, err = newIssue("same_multihop_endpoint", "multihop.exit", profileID, nil)
				if err != nil {
					return Snapshot{}, err
				}
			}
			snapshot.Multihop.Bindings = append(snapshot.Multihop.Bindings, Binding{ProfileID: profileID, Entry: entry, Exit: exit})
		}
		if len(snapshot.ProfileOrder) == 0 {
			if _, err := newIssue("missing_profile", "multihop", "", nil); err != nil {
				return Snapshot{}, err
			}
		}
	}
	if bundle.LastGood.Present {
		var hints map[string]string
		if checkJSON(bundle.LastGood.Bytes) != nil || json.Unmarshal(bundle.LastGood.Bytes, &hints) != nil || hints == nil {
			if _, err := newIssue("corrupt_hint_cache", "lastGood", "", nil); err != nil {
				return Snapshot{}, err
			}
		} else {
			keys := make([]string, 0, len(hints))
			for id := range hints {
				keys = append(keys, id)
			}
			sort.Strings(keys)
			for _, id := range keys {
				ref, err := resolve(id, hints[id], "lastGood")
				if err != nil {
					return Snapshot{}, err
				}
				snapshot.LastGood[id] = ref
			}
		}
	}
	if err := snapshot.Validate(); err != nil {
		return Snapshot{}, err
	}
	// Source model maps and pointers belong to the decoded candidate, never to
	// a committed repository. Return one fully detached, validated snapshot.
	return cloneSnapshot(snapshot)
}

type legacyConnection struct {
	Autoconnect bool   `json:"autoconnect"`
	LastProfile string `json:"last_profile"`
	LastNode    string `json:"last_node"`
}
type legacyMultihop struct {
	Enabled         bool
	EntryID, ExitID string
}

func migratePreferences(file LegacyFile) (Preferences, legacyConnection, legacyMultihop, error) {
	preferences := Preferences{Version: PreferencesVersion}
	if !file.Present {
		return preferences, legacyConnection{}, legacyMultihop{}, nil
	}
	fields, err := objectFields(file.Bytes)
	if err != nil {
		return Preferences{}, legacyConnection{}, legacyMultihop{}, err
	}
	var version int
	if json.Unmarshal(fields["version"], &version) != nil || (version != 1 && version != 2) {
		return Preferences{}, legacyConnection{}, legacyMultihop{}, ErrUnsupportedSchema
	}
	var intent legacyConnection
	var mh struct {
		Enabled bool   `json:"multihop"`
		EntryID string `json:"multihop_entry_id"`
		ExitID  string `json:"multihop_exit_id"`
	}
	if json.Unmarshal(file.Bytes, &intent) != nil || json.Unmarshal(file.Bytes, &mh) != nil {
		return Preferences{}, legacyConnection{}, legacyMultihop{}, ErrCorruptState
	}
	if err := validateShape(file.Bytes, reflect.TypeOf(intent), 0); err != nil {
		return Preferences{}, legacyConnection{}, legacyMultihop{}, err
	}
	if err := validateShape(file.Bytes, reflect.TypeOf(mh), 0); err != nil {
		return Preferences{}, legacyConnection{}, legacyMultihop{}, err
	}
	for _, key := range legacyIntentKeys {
		delete(fields, key)
	}
	fields["version"] = json.RawMessage("3")
	data, err := json.Marshal(fields)
	if err != nil || json.Unmarshal(data, &preferences) != nil {
		return Preferences{}, legacyConnection{}, legacyMultihop{}, ErrCorruptState
	}
	if version == 1 {
		off := false
		otherOff := false
		preferences.PresetGamesDirect = &off
		preferences.PresetVoiceDirect = &otherOff
	}
	return preferences, intent, legacyMultihop{mh.Enabled, mh.EntryID, mh.ExitID}, nil
}
