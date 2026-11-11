package control

import (
	"context"
	"errors"
	"reflect"

	"github.com/Divaaaan/tenebra/core/model"
	"github.com/Divaaaan/tenebra/core/profile"
	"github.com/Divaaaan/tenebra/core/protection"
	"github.com/Divaaaan/tenebra/core/routing"
	"github.com/Divaaaan/tenebra/core/singbox"
	"github.com/Divaaaan/tenebra/core/state"
)

func canonicalError(id int64, err error) Response {
	code := "persistence_error"
	switch {
	case errors.Is(err, state.ErrMaintenancePending):
		code = "maintenance_pending"
	case errors.Is(err, state.ErrProfileConflict), errors.Is(err, profile.ErrNotFound):
		code = "profile_stale"
	case errors.Is(err, state.ErrIntentConflict):
		code = "intent_stale"
	case errors.Is(err, state.ErrNeedsSelection):
		code = "reselect_required"
	case errors.Is(err, state.ErrRequiredReference):
		code = "required_reference"
	}
	return newError(id, code)
}
func (d *Daemon) handleCanonicalRequest(ctx context.Context, req Request) (Response, bool) {
	switch req.Cmd {
	case CmdImportSubscription, CmdRefreshSub:
		return d.handleCanonicalLegacyNetwork(ctx, req), true
	case CmdStartSubscriptionOperation, CmdContinueSubscriptionOperation, CmdSubscriptionOperationStatus, CmdCancelSubscriptionOperation:
		view, err := d.handleSubscriptionOperation(ctx, req)
		if err != nil {
			return newError(req.ID, subscriptionErrorCode(err)), true
		}
		result, err := newResult(req.ID, view)
		if err != nil {
			return newError(req.ID, "storage_error"), true
		}
		return result, true
	case CmdConnect, CmdSetMultihop:
		return newError(req.ID, "reselect_required"), true
	case CmdConnectV2:
		if err := d.canonical.Ready(); err != nil {
			return canonicalError(req.ID, err), true
		}
		if req.Selection == nil || req.Node != "" {
			return newError(req.ID, "reselect_required"), true
		}
		selection := req.Selection
		switch selection.Kind {
		case string(state.Auto):
			if selection.Ref != nil {
				return newError(req.ID, "reselect_required"), true
			}
		case string(state.Pinned):
			ref := selection.Ref
			if ref == nil || ref.ProfileID != req.Profile || ref.Resolution != string(state.Resolved) || ref.IssueID != "" {
				return newError(req.ID, "reselect_required"), true
			}
			p, ok := d.store.Get(req.Profile)
			if !ok {
				return newError(req.ID, "profile_stale"), true
			}
			if _, _, err := profile.ResolveNode(p, ref.NodeID); err != nil {
				return newError(req.ID, "reselect_required"), true
			}
			req.Node = ref.NodeID
		default:
			return newError(req.ID, "reselect_required"), true
		}
		// Persist the user's typed intent before any native preparation or
		// candidate work. An older successful loop must not write it back later.
		wanted := state.Selection{Kind: state.SelectionKind(selection.Kind)}
		if wanted.Kind == state.Pinned {
			wanted.Ref = state.NodeRef{ProfileID: req.Profile, NodeID: selection.Ref.NodeID, Resolution: state.Resolved}
		}
		_, err := d.CommitCanonical(ctx, state.AnyRevision, func(snapshot *state.Snapshot) error {
			current, ok := snapshot.Profiles[req.Profile]
			if !ok {
				return state.ErrProfileConflict
			}
			if wanted.Kind == state.Pinned {
				if _, _, err := profile.ResolveNode(current, wanted.Ref.NodeID); err != nil {
					return state.ErrNeedsSelection
				}
			}
			snapshot.Connection.ProfileID, snapshot.Connection.Selection = req.Profile, wanted
			return nil
		})
		if err != nil {
			return canonicalError(req.ID, err), true
		}
		return d.handleConnect(ctx, req), true
	case CmdSetMultihopV2:
		return d.handleCanonicalMultihop(ctx, req), true
	case CmdSetSubscriptionHTTPPolicy:
		if req.AllowHTTP == nil || req.ProfileRevision == 0 || req.ProfileRevision > 9007199254740991 {
			return newError(req.ID, "invalid_request"), true
		}
		_, err := d.CommitCanonical(ctx, state.AnyRevision, func(s *state.Snapshot) error {
			p, ok := s.Profiles[req.Profile]
			if !ok || p.Source != profile.SourceSubscription || p.Revision != req.ProfileRevision {
				return state.ErrProfileConflict
			}
			p.HTTPAllowed = *req.AllowHTTP
			s.Profiles[p.ID] = p
			return nil
		})
		if err != nil {
			return canonicalError(req.ID, err), true
		}
		return newResult0(req.ID), true
	case CmdSetRouting, CmdSetSplit, CmdSetPresets, CmdSetKillSwitch, CmdSetTLSFragment, CmdSetProxyMode, CmdSetTun, CmdSetAutoconnect, CmdSetAutoFailover, CmdSetDNS, CmdSetRules, CmdSetCrashReports, CmdSetZapretAutoUpdate:
		return d.handleCanonicalPreference(ctx, req), true
	}
	return Response{}, false
}
func canonicalPreferenceMutation(s *state.Snapshot, req Request) error {
	p := &s.Preferences
	switch req.Cmd {
	case CmdSetRouting:
		mode := routing.Mode(req.Mode)
		if mode != routing.ModeSmart && mode != routing.ModeGlobal && mode != routing.ModeDirect {
			return state.ErrCorruptState
		}
		p.RoutingMode = req.Mode
	case CmdSetSplit:
		mode := routing.SplitMode(req.Mode)
		if mode != routing.SplitOff && mode != routing.SplitExclude && mode != routing.SplitInclude {
			return state.ErrCorruptState
		}
		ro := routing.Options{SplitMode: mode, SplitApps: append([]string(nil), req.Apps...)}.Normalize()
		p.SplitMode, p.SplitApps = string(ro.SplitMode), ro.SplitApps
	case CmdSetPresets:
		if req.Games == nil && req.Voice == nil && req.Services == nil {
			return state.ErrCorruptState
		}
		if req.Games != nil {
			v := *req.Games
			p.PresetGamesDirect = &v
		}
		if req.Voice != nil {
			v := *req.Voice
			p.PresetVoiceDirect = &v
		}
		if req.Services != nil {
			v := *req.Services
			p.PresetUnblockServices = &v
		}
	case CmdSetKillSwitch:
		p.KillSwitch = req.On
	case CmdSetTLSFragment:
		p.TLSFragment = req.On
	case CmdSetProxyMode:
		if !singbox.ValidMode(req.ProxyMode) || req.ProxyPort < 0 || req.ProxyPort > 65535 {
			return state.ErrCorruptState
		}
		p.ProxyMode = req.ProxyMode
		if req.ProxyPort != 0 {
			p.ProxyPort = req.ProxyPort
		}
	case CmdSetTun:
		if !singbox.ValidStack(req.Stack) {
			return state.ErrCorruptState
		}
		p.TunStack = req.Stack
	case CmdSetAutoconnect:
		s.Connection.Autoconnect = req.On
	case CmdSetAutoFailover:
		value := req.On
		p.AutoFailover = &value
	case CmdSetCrashReports:
		value := req.On
		p.CrashReports = &value
	case CmdSetZapretAutoUpdate:
		value := req.On
		p.ZapretAutoUpdate = &value
	case CmdSetDNS:
		if !routing.ValidDNSServer(req.DNSRemote) || !routing.ValidDNSServer(req.DNSDirect) {
			return state.ErrCorruptState
		}
		ro := routing.Options{DNSRemote: req.DNSRemote, DNSDirect: req.DNSDirect}.Normalize()
		p.AdBlock, p.IPv4Only, p.DNSRemote, p.DNSDirect = req.AdBlock, req.IPv4Only, ro.DNSRemote, ro.DNSDirect
	case CmdSetRules:
		for _, values := range [][]string{req.RulesDirect, req.RulesProxy} {
			for _, domain := range values {
				if !routing.ValidDomainSuffix(domain) {
					return state.ErrCorruptState
				}
			}
		}
		ro := routing.Options{RulesDirect: append([]string(nil), req.RulesDirect...), RulesProxy: append([]string(nil), req.RulesProxy...)}.Normalize()
		p.RulesDirect, p.RulesProxy, p.PresetRuBanking, p.PresetRuGov = ro.RulesDirect, ro.RulesProxy, req.PresetRuBanking, req.PresetRuGov
	default:
		return state.ErrCorruptState
	}
	if p.KillSwitch && (req.Cmd == CmdSetDNS || req.Cmd == CmdSetKillSwitch) {
		ro := routing.Options{DNSDirect: p.DNSDirect}.Normalize()
		if err := protection.ValidateDNS(ro.DNSDirect); err != nil {
			return state.ErrCorruptState
		}
	}
	return nil
}
func (d *Daemon) handleCanonicalPreference(ctx context.Context, req Request) Response {
	// Validate against a disposable copy before reporting any storage error.
	check := d.canonical.View()
	if err := canonicalPreferenceMutation(&check, req); err != nil {
		return newError(req.ID, "invalid_request")
	}
	// The repository writer is a separate lane. Its pure mutation validates
	// the latest saved DNS/kill-switch pair; waiting for disk must not hold
	// protectionOp and thereby prevent an independent disconnect.
	if req.Cmd == CmdSetDNS {
		if _, required := d.ProtectionDNS(); required {
			candidate := routing.Options{DNSDirect: req.DNSDirect}.Normalize()
			if err := protection.ValidateDNS(candidate.DNSDirect); err != nil {
				return newError(req.ID, "invalid_request")
			}
		}
	}
	changed := false
	_, err := d.CommitCanonical(ctx, state.AnyRevision, func(s *state.Snapshot) error {
		beforePrefs, beforeIntent := s.Preferences, s.Connection
		if err := canonicalPreferenceMutation(s, req); err != nil {
			return err
		}
		changed = !reflect.DeepEqual(beforePrefs, s.Preferences) || beforeIntent != s.Connection
		return nil
	})
	if err != nil {
		return canonicalError(req.ID, err)
	}
	var guardErr error
	if req.Cmd == CmdSetKillSwitch {
		d.protectionOp.Lock()
		// A newer command may have committed while this command waited for the
		// native lane. Converge to the current coherent desired view, rather
		// than applying this older request over the newer preference.
		currentRouting := d.snapshotRouting()
		if !currentRouting.KillSwitch {
			guardErr = d.protection.Release()
		} else if !d.protection.LegacyEngineOnly() {
			cur := d.snapshotState()
			before := d.protection.Snapshot()
			if before.Status != "active" && (cur.State == StateConnected || cur.State == StateConnecting || cur.Protection.Enforced || before.Status == "error") {
				if err := protection.ValidateDNS(currentRouting.Normalize().DNSDirect); err != nil {
					guardErr = d.protection.Reject(err)
				} else {
					guardErr = d.protection.Prepare()
				}
			}
		}
		d.protectionOp.Unlock()
	}
	if guardErr != nil {
		return newError(req.ID, "protection_error")
	}
	if changed {
		switch req.Cmd {
		case CmdSetAutoconnect, CmdSetAutoFailover, CmdSetCrashReports, CmdSetZapretAutoUpdate:
		default:
			outcome := d.reapplyLatest(ctx)
			if outcome.Err != nil {
				return d.reapplyRefusalResponse(req.ID, outcome)
			}
		}
	}
	response, resultErr := newResult(req.ID, d.snapshotState())
	if resultErr != nil {
		return newError(req.ID, "internal_error")
	}
	return response
}
func (d *Daemon) handleCanonicalMultihop(ctx context.Context, req Request) Response {
	cur := d.snapshotState()
	if req.Enabled && cur.State == StateConnected && cur.Profile != req.Profile {
		return newError(req.ID, "invalid_request")
	}
	_, err := d.CommitCanonical(ctx, state.AnyRevision, func(s *state.Snapshot) error {
		p, ok := s.Profiles[req.Profile]
		if !ok {
			return state.ErrProfileConflict
		}
		if req.Enabled {
			if req.EntryID == req.ExitID {
				return state.ErrNeedsSelection
			}
			if _, _, err := profile.ResolveNode(p, req.EntryID); err != nil {
				return state.ErrNeedsSelection
			}
			if _, _, err := profile.ResolveNode(p, req.ExitID); err != nil {
				return state.ErrNeedsSelection
			}
			mh := model.Multihop{Enabled: true, EntryID: req.EntryID, ExitID: req.ExitID}
			if err := validateMultihopProfile(p, mh); err != nil {
				return state.ErrNeedsSelection
			}
			binding := state.Binding{ProfileID: p.ID, Entry: state.NodeRef{ProfileID: p.ID, NodeID: req.EntryID, Resolution: state.Resolved}, Exit: state.NodeRef{ProfileID: p.ID, NodeID: req.ExitID, Resolution: state.Resolved}}
			found := false
			for i, b := range s.Multihop.Bindings {
				if b.ProfileID == p.ID {
					s.Multihop.Bindings[i] = binding
					found = true
					break
				}
			}
			if !found {
				s.Multihop.Bindings = append(s.Multihop.Bindings, binding)
			}
		}
		s.Multihop.Enabled = req.Enabled
		return nil
	})
	if err != nil {
		return canonicalError(req.ID, err)
	}
	if outcome := d.reapplyLatest(ctx); outcome.Err != nil {
		return d.reapplyRefusalResponse(req.ID, outcome)
	}
	response, resultErr := newResult(req.ID, d.snapshotState())
	if resultErr != nil {
		return newError(req.ID, "internal_error")
	}
	return response
}
