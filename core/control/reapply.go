package control

import (
	"context"
	"fmt"

	"github.com/Divaaaan/tenebra/core/profile"
	"github.com/Divaaaan/tenebra/core/routing"
	"github.com/Divaaaan/tenebra/core/singbox"
	"github.com/Divaaaan/tenebra/core/state"
)

type connectPlan struct {
	ProfileID, NodeID, SelectorTag string
	ConfigJSON                     []byte
	ConfigRevision                 uint64
	tags                           map[string]string
}

func (connectPlan) String() string { return "<private connect preflight>" }

type reapplyOutcome struct {
	Stage            string
	Applied, Pending bool
	Err              error
}

func (d *Daemon) reapplyRefusalResponse(id int64, outcome reapplyOutcome) Response {
	code := "apply_failed"
	if outcome.Stage == "preflight" {
		code = "apply_preflight_refused"
	}
	response := newError(id, code)
	if stateResponse, err := newResult(id, d.snapshotState()); err == nil {
		response.Data = stateResponse.Data
	}
	return response
}

func makeConnectPlan(p profile.Profile, nodeID string, ro routing.Options, tun singbox.TunOptions, revision uint64) (connectPlan, error) {
	tags := serverTags(p)
	tag, ok := tags[nodeID]
	if !ok {
		return connectPlan{}, profile.ErrNodeUnusable
	}
	body, _, err := buildConfigJSON(profileNodes(p), tag, ro, tun)
	if err != nil {
		return connectPlan{}, err
	}
	return connectPlan{ProfileID: p.ID, NodeID: nodeID, SelectorTag: tag, ConfigJSON: body, ConfigRevision: revision, tags: tags}, nil
}

// prepareConnect validates and renders immutable input before any native
// protection action, process stop or selector change.
func (d *Daemon) prepareConnect(ctx context.Context, p profile.Profile, selection state.Selection) (connectPlan, error) {
	if err := ctx.Err(); err != nil {
		return connectPlan{}, err
	}
	var nodeID string
	switch selection.Kind {
	case state.Auto:
	case state.Pinned:
		if selection.Ref.ProfileID != p.ID || selection.Ref.Resolution != state.Resolved {
			return connectPlan{}, state.ErrNeedsSelection
		}
		nodeID = selection.Ref.NodeID
	default:
		return connectPlan{}, state.ErrNeedsSelection
	}
	d.mu.Lock()
	mh := d.multihop
	d.mu.Unlock()
	if d.canonical != nil {
		var err error
		mh, err = d.canonicalMultihop(p.ID)
		if err != nil {
			return connectPlan{}, err
		}
	}
	if err := validateMultihopProfile(p, mh); err != nil {
		return connectPlan{}, err
	}
	effectiveNode := nodeID
	if mh.Enabled {
		effectiveNode = mh.ExitID
	}
	candidates, err := buildCandidates(p, effectiveNode)
	if err != nil {
		return connectPlan{}, err
	}
	ro, tun, _, revision, err := d.captureConnectionInputs(p, nodeID, false, mh)
	if err != nil {
		return connectPlan{}, err
	}
	return makeConnectPlan(p, candidates[0].NodeID, resolveMultihop(ro, mh, serverTags(p)), tun, revision)
}

func (d *Daemon) reapplyLive() reapplyOutcome { return d.reapplyLatest(context.Background()) }

func (d *Daemon) reapplyLatest(ctx context.Context) reapplyOutcome {
	d.connMu.Lock()
	defer d.connMu.Unlock()
	current := d.snapshotState()
	out := reapplyOutcome{Stage: "preflight", Pending: current.SettingsPending}
	if current.State != StateConnected || current.Profile == "" || current.Node == "" {
		return out
	}
	refuse := func(err error) reapplyOutcome {
		d.emitLog(LogWarn, "re-apply deferred: preflight refused; current process retained")
		view := d.snapshotState()
		d.mu.Lock()
		emit := d.emit
		d.mu.Unlock()
		if emit != nil {
			emit(EventState, stateEventBody(view))
		}
		return reapplyOutcome{Stage: "preflight", Pending: view.SettingsPending, Err: err}
	}
	p, exists := d.store.Get(current.Profile)
	if !exists {
		return refuse(profile.ErrNotFound)
	}
	node, err := d.desiredReconnectNode(current.Profile, current.Node)
	if err != nil {
		return refuse(err)
	}
	selection := state.Selection{Kind: state.Pinned, Ref: state.NodeRef{ProfileID: p.ID, NodeID: node, Resolution: state.Resolved}}
	if _, err = d.prepareConnect(ctx, p, selection); err != nil {
		return refuse(err)
	}
	if !d.snapshotState().SettingsPending {
		return reapplyOutcome{Stage: "accepted", Applied: true}
	}
	stage := "preflight"
	view, err := d.startConnectTracked(ctx, p, node, false, false, "", &stage)
	if err != nil {
		if stage == "preflight" {
			return refuse(err)
		}
		d.emitLog(LogError, fmt.Sprintf("re-apply failed after replacement began: %v", err))
		d.setState(State{State: StateError, Profile: current.Profile, Node: current.Node,
			Error: "re-apply failed after replacement began", Routing: d.snapshotState().Routing})
		return reapplyOutcome{Stage: "swapped", Pending: d.snapshotState().SettingsPending, Err: err}
	}
	if view.State == StateConnected && !view.SettingsPending {
		return reapplyOutcome{Stage: "accepted", Applied: true}
	}
	return reapplyOutcome{Stage: "swapped", Pending: view.SettingsPending}
}
