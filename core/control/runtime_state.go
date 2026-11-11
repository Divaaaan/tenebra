package control

import (
	"crypto/rand"
	"errors"
	"reflect"

	"github.com/Divaaaan/tenebra/core/protection"
	"github.com/Divaaaan/tenebra/core/state"
)

const maxPublicRevision uint64 = 9007199254740991

func nextPublicRevision(current uint64) (uint64, error) {
	if current >= maxPublicRevision {
		return 0, errors.New("runtime revision exhausted")
	}
	return current + 1, nil
}

func advancePublicRevision(current uint64) uint64 {
	next, err := nextPublicRevision(current)
	if err != nil {
		// A new daemon instance is required; never publish a wrapped authority
		// stamp or a number that JavaScript cannot represent exactly.
		panic(err)
	}
	return next
}

func (d *Daemon) initializeRuntimeView() {
	d.runtimeInstanceID = rand.Text()
	d.publicRevision, d.configRevision, d.appliedConfigRevision = 1, 1, 1
	d.desiredConfig = captureConfigIntent(d.routing, d.tun, state.ConnectionIntent{Selection: state.Selection{Kind: state.Auto}}, state.MultihopRequirement{})
	d.desiredConfigSet = true
	d.state.Protection, d.protectionEpoch = d.protection.SnapshotWithEpoch()
	d.commitViewLocked(nil)
}

func (d *Daemon) InstanceID() string { d.mu.Lock(); defer d.mu.Unlock(); return d.runtimeInstanceID }

func (d *Daemon) updateDesiredConfigLocked(intent configIntent) {
	if !d.desiredConfigSet {
		d.desiredConfig, d.desiredConfigSet = intent, true
		return
	}
	if d.desiredConfig.Equal(intent) {
		return
	}
	d.configRevision = advancePublicRevision(d.configRevision)
	d.desiredConfig = intent
}

func (d *Daemon) syncDesiredOptionsLocked() {
	ro := d.routing
	chain := d.desiredConfig.multihop
	if d.canonical == nil {
		chain = configMultihop(d.desiredConfig.connection.ProfileID, d.multihop)
	}
	ro.Multihop = chain.Enabled
	intent := captureConfigIntent(ro, d.tun, d.desiredConfig.connection, chain)
	intent.nodeRecords = append([]string(nil), d.desiredConfig.nodeRecords...)
	d.updateDesiredConfigLocked(intent)
}

// commitViewLocked assigns the stamp to its owned coherent view, never to an
// older copy in an encoder. The caller owns d.mu (or construction exclusively).
func (d *Daemon) commitViewLocked(mutate func(*State)) State {
	if mutate != nil {
		mutate(&d.state)
	}
	view := cloneIdentityView(d.state)
	if d.canonical != nil {
		view.Selection, view.MultihopV2 = d.canonicalSelection, d.canonicalMultihopView
		view = cloneIdentityView(view)
	}
	if view.State != StateConnecting && view.State != StateConnected {
		d.appliedConfigRevision = d.configRevision
	}
	view.DaemonInstanceID, view.StateRevision = d.runtimeInstanceID, d.publicRevision
	view.ConfigRevision, view.AppliedConfigRevision = d.configRevision, d.appliedConfigRevision
	view.SettingsPending = view.ConfigRevision != view.AppliedConfigRevision
	// Protocol capabilities are enabled only when their complete production
	// wiring passes its acceptance tests; private primitives are insufficient.
	view.Capabilities = &Capabilities{StateRevisionsV1: d.runtimeRevisionsReady, IdentitySelectionV2: d.canonical != nil}
	if d.lastPublicView != nil && !reflect.DeepEqual(*d.lastPublicView, view) {
		d.publicRevision = advancePublicRevision(d.publicRevision)
		view.StateRevision = d.publicRevision
	}
	d.state = view
	remember := cloneIdentityView(view)
	d.lastPublicView = &remember
	return cloneIdentityView(view)
}

// applyProtectionLocked rejects callbacks captured before a newer native policy
// transition. The guard owns the stamp; a delayed callback cannot mint one.
func (d *Daemon) applyProtectionLocked(snapshot protection.State, epoch uint64) bool {
	if epoch == 0 || epoch <= d.protectionEpoch {
		return false
	}
	d.protectionEpoch = epoch
	d.state.Protection = snapshot
	return true
}

func (d *Daemon) applyProtectionSnapshot(snapshot protection.State, epoch uint64) (State, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	changed := d.applyProtectionLocked(snapshot, epoch)
	return d.commitViewLocked(nil), changed
}
