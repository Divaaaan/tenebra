package control

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/Divaaaan/tenebra/core/model"
	"github.com/Divaaaan/tenebra/core/state"
)

func (d *Daemon) CanonicalReady() error {
	if d.canonical == nil {
		return nil
	}
	return d.canonical.Ready()
}

func (d *Daemon) SetCanonicalState(store *state.Store) error {
	if store == nil {
		return state.ErrCorruptState
	}
	snapshot := store.View()
	if err := snapshot.Validate(); err != nil {
		return err
	}
	d.mu.Lock()
	if d.canonical != nil {
		d.mu.Unlock()
		return errors.New("canonical repository already installed")
	}
	d.canonical = store
	d.store = store.Profiles()
	d.settings = nil
	lastGood := newCanonicalLastGood(d)
	d.lastGood = lastGood
	d.mu.Unlock()
	lastGood.start()
	d.projectRepository(snapshot)
	return nil
}
func (d *Daemon) CommitCanonical(ctx context.Context, revision uint64, mutate func(*state.Snapshot) error) (state.Snapshot, error) {
	if d.canonical == nil {
		return state.Snapshot{}, state.ErrCorruptState
	}
	snapshot, err := d.canonical.Transact(ctx, revision, mutate)
	if err != nil {
		return state.Snapshot{}, err
	}
	d.projectRepository(snapshot)
	return snapshot, nil
}
func (d *Daemon) projectRepository(snapshot state.Snapshot) (State, bool) {
	// The caller's committed return value is mutable. Projection owns a separate
	// decoded copy, including pointer flags and slices retained by live options.
	data, err := json.Marshal(snapshot)
	if err != nil {
		return d.snapshotState(), false
	}
	var owned state.Snapshot
	if json.Unmarshal(data, &owned) != nil || owned.Validate() != nil {
		return d.snapshotState(), false
	}
	settingsData, err := json.Marshal(owned.Preferences)
	if err != nil {
		return d.snapshotState(), false
	}
	var ps persistedSettings
	if json.Unmarshal(settingsData, &ps) != nil {
		return d.snapshotState(), false
	}
	ps.Autoconnect = owned.Connection.Autoconnect
	intent, err := connectionIntent(owned)
	if err != nil {
		return d.snapshotState(), false
	}
	ps.LastProfile = owned.Connection.ProfileID
	if owned.Connection.Selection.Kind == state.Pinned && owned.Connection.Selection.Ref.Resolution == state.Resolved {
		ps.LastNode = owned.Connection.Selection.Ref.NodeID
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if owned.Revision <= d.lastProjectedRevision {
		return cloneIdentityView(d.state), false
	}
	profileID := d.state.Profile
	if profileID == "" {
		profileID = owned.Connection.ProfileID
	}
	ps.Multihop = owned.Multihop.Enabled
	if binding, err := owned.BindingFor(profileID); err == nil {
		ps.MultihopEntryID, ps.MultihopExitID = binding.Entry.NodeID, binding.Exit.NodeID
	}
	d.applyPersistedSettingsLocked(ps)
	effectiveRouting := d.routing
	effectiveRouting.Multihop = owned.Multihop.Enabled
	effectiveIntent := captureConfigIntent(effectiveRouting, d.tun, owned.Connection, owned.Multihop)
	effectiveIntent.nodeRecords = intent.nodeRecords
	d.updateDesiredConfigLocked(effectiveIntent)
	d.lastProjectedRevision = owned.Revision
	d.state.Selection = selectionView(owned.Connection.Selection)
	d.state.MultihopV2 = multihopView(owned.Multihop)
	d.canonicalSelection, d.canonicalMultihopView = d.state.Selection, d.state.MultihopV2
	return d.commitViewLocked(nil), true
}
func (d *Daemon) canonicalMultihop(profileID string) (model.Multihop, error) {
	snapshot := d.canonical.View()
	if !snapshot.Multihop.Enabled {
		return model.Multihop{}, nil
	}
	binding, err := snapshot.BindingFor(profileID)
	if err != nil {
		return model.Multihop{}, err
	}
	return model.Multihop{Enabled: true, EntryID: binding.Entry.NodeID, ExitID: binding.Exit.NodeID}, nil
}
