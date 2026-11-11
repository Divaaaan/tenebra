package control

import (
	"github.com/Divaaaan/tenebra/core/model"
	"github.com/Divaaaan/tenebra/core/profile"
	"github.com/Divaaaan/tenebra/core/routing"
	"github.com/Divaaaan/tenebra/core/singbox"
	"github.com/Divaaaan/tenebra/core/state"
)

func configMultihop(profileID string, mh model.Multihop) state.MultihopRequirement {
	chain := state.MultihopRequirement{Enabled: mh.Enabled}
	if mh.Enabled {
		chain.Bindings = []state.Binding{{ProfileID: profileID,
			Entry: state.NodeRef{ProfileID: profileID, NodeID: mh.EntryID, Resolution: state.Resolved},
			Exit:  state.NodeRef{ProfileID: profileID, NodeID: mh.ExitID, Resolution: state.Resolved}}}
	}
	return chain
}

func (d *Daemon) desiredReconnectNode(profileID, fallback string) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	connection := d.desiredConfig.connection
	if connection.ProfileID != profileID {
		return "", state.ErrIntentConflict
	}
	if connection.Selection.Kind == state.Pinned {
		if connection.Selection.Ref.Resolution != state.Resolved || connection.Selection.Ref.ProfileID != profileID {
			return "", state.ErrNeedsSelection
		}
		return connection.Selection.Ref.NodeID, nil
	}
	return fallback, nil
}

// captureConnectionInputs stamps the actual immutable profile/options before
// native preparation, candidate probes and per-run bindings. An old profile or
// resolved chain cannot borrow the revision of a newer desired configuration.
func (d *Daemon) captureConnectionInputs(p profile.Profile, requestedNode string, remember bool, mh model.Multihop) (routing.Options, singbox.TunOptions, configIntent, uint64, error) {
	records, err := configNodeRecords(p)
	if err != nil {
		return routing.Options{}, singbox.TunOptions{}, configIntent{}, 0, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	ro, tun := d.routing, d.tun
	ro.Multihop = mh.Enabled
	connection := d.desiredConfig.connection
	chain := configMultihop(p.ID, mh)
	if d.canonical == nil && (remember || connection.ProfileID != p.ID) {
		connection = state.ConnectionIntent{ProfileID: p.ID, Selection: state.Selection{Kind: state.Auto}}
		if requestedNode != "" {
			connection.Selection = state.Selection{Kind: state.Pinned, Ref: state.NodeRef{ProfileID: p.ID, NodeID: requestedNode, Resolution: state.Resolved}}
		}
	}
	intent := captureConfigIntent(ro, tun, connection, chain)
	intent.nodeRecords = records
	if d.canonical == nil {
		d.updateDesiredConfigLocked(intent)
	} else {
		d.syncDesiredOptionsLocked()
		if connection.ProfileID != p.ID || !intent.Equal(d.desiredConfig) ||
			(!mh.Enabled && connection.Selection.Kind == state.Pinned && connection.Selection.Ref.NodeID != requestedNode) {
			return routing.Options{}, singbox.TunOptions{}, configIntent{}, 0, state.ErrIntentConflict
		}
	}
	return ro, tun, intent, d.configRevision, nil
}

func (d *Daemon) recordAppliedConfig(loop fallbackLoop) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.syncDesiredOptionsLocked()
	if loop.intent.Equal(d.desiredConfig) {
		d.appliedConfigRevision = d.configRevision
	} else if loop.configRevision != 0 {
		d.appliedConfigRevision = loop.configRevision
	}
}
