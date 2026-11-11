package control

import (
	"context"
	"errors"
	"testing"

	"github.com/Divaaaan/tenebra/core/model"
	"github.com/Divaaaan/tenebra/core/profile"
	"github.com/Divaaaan/tenebra/core/state"
)

func TestReapplyPreflightFailureKeepsLiveEngine(t *testing.T) {
	d, runner, p := coreAuditDaemon(t)
	coreAuditConnect(t, d, p, p.Servers[0].ID)
	bad := p
	bad.Servers = append([]profile.Server(nil), p.Servers...)
	bad.Servers[0].Node.Protocol = model.Protocol("unsupported-retained-record")
	if err := d.store.Update(bad); err != nil {
		t.Fatal(err)
	}
	starts, stops := runner.starts(), runner.stops()
	d.Handle(context.Background(), Request{Cmd: CmdSetTLSFragment, On: true})
	current := d.snapshotState()
	if runner.starts() != starts || runner.stops() != stops {
		t.Fatal("preflight refusal restarted live engine")
	}
	if current.State != StateConnected || !current.SettingsPending {
		t.Fatal("preflight refusal reported a healthy old engine down or lost pending", current.State, current.SettingsPending)
	}
}

func TestReapplyUnchangedAcceptedConfigDoesNotRestart(t *testing.T) {
	d, runner, p := coreAuditDaemon(t)
	coreAuditConnect(t, d, p, p.Servers[0].ID)
	starts, stops := runner.starts(), runner.stops()
	outcome := d.reapplyLatest(context.Background())
	if outcome.Stage != "accepted" || !outcome.Applied || outcome.Pending || outcome.Err != nil || runner.starts() != starts || runner.stops() != stops {
		t.Fatal("unchanged accepted config was swapped", outcome)
	}
}

func TestCanonicalReapplyRefusalReturnsTypedPendingState(t *testing.T) {
	d, runner, _, release := canonicalRuntimeProbeGate(t)
	release()
	waitFor(t, func() bool { return d.snapshotState().State == StateConnected }, "accepted initial engine")
	other := addCanonicalProfile(t, d.canonical)
	_, err := d.CommitCanonical(context.Background(), state.AnyRevision, func(s *state.Snapshot) error {
		s.Connection = state.ConnectionIntent{ProfileID: other.ID, Selection: state.Selection{Kind: state.Pinned, Ref: state.NodeRef{ProfileID: other.ID, NodeID: other.Servers[0].ID, Resolution: state.Resolved}}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	starts, stops := runner.starts(), runner.stops()
	response := d.Handle(context.Background(), Request{Cmd: CmdSetTLSFragment, On: true})
	if response.Ok || response.Error != "apply_preflight_refused" || runner.starts() != starts || runner.stops() != stops {
		t.Fatal("preflight refusal was not typed or swapped the live process", response.Error)
	}
	current := d.snapshotState()
	if current.State != StateConnected || !current.SettingsPending || !current.TLSFragment {
		t.Fatal("saved desired setting/live state lost", current.State, current.SettingsPending)
	}
}

func TestReapplyAfterTeardownFailureReportsError(t *testing.T) {
	d, runner, p := coreAuditDaemon(t)
	coreAuditConnect(t, d, p, p.Servers[0].ID)
	stops := runner.stops()
	runner.mu.Lock()
	runner.startErr = errors.New("controlled replacement refusal")
	runner.mu.Unlock()
	d.Handle(context.Background(), Request{Cmd: CmdSetTLSFragment, On: true})
	waitFor(t, func() bool { return d.snapshotState().State == StateError }, "honest failed replacement state")
	if runner.stops() <= stops {
		t.Fatal("replacement fixture did not tear down old engine")
	}
}
