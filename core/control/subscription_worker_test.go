package control

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Divaaaan/tenebra/core/maintenance"
	"github.com/Divaaaan/tenebra/core/profile"
	"github.com/Divaaaan/tenebra/core/state"
	"github.com/Divaaaan/tenebra/core/subscription"
)

var syntheticOperationBody = []byte("trojan://SYNTHETIC_ONLY@node.test:443?security=tls&sni=sni.test#refreshed")

func subscriptionWorkerFixture(t *testing.T) (*Daemon, *state.Store, profile.Profile, context.Context) {
	t.Helper()
	d, store, _ := newCanonicalTestDaemon(t)
	p := addCanonicalProfile(t, store)
	ctx := subscriptionTestSession(t, d)
	d.subscriptionOps.accepted = subscriptionAcceptedInfo{ConfirmedUnprotected: true}
	d.subscriptionOps.network = func(_ context.Context, lease *maintenance.Lease) (func(), error) {
		return func() {}, lease.BindResolver(func(context.Context, string) ([]net.IP, error) { return []net.IP{net.ParseIP("93.184.216.34")}, nil })
	}
	t.Cleanup(d.subscriptionOps.close)
	return d, store, p, ctx
}

func startDefaultSubscription(t *testing.T, d *Daemon, ctx context.Context, id string) SubscriptionOperationView {
	t.Helper()
	prepared, err := d.startSubscriptionOperation(ctx, SubscriptionStart{Kind: "refresh", ProfileID: id})
	if err != nil {
		t.Fatal(err)
	}
	running, err := d.continueSubscriptionOperation(ctx, prepared.OperationID, "default", "")
	if err != nil || running.Stage != "running" {
		t.Fatal(running, err)
	}
	return running
}

func waitSubscriptionTerminal(t *testing.T, d *Daemon, ctx context.Context, id string) SubscriptionOperationView {
	t.Helper()
	var view SubscriptionOperationView
	waitFor(t, func() bool {
		current, err := d.subscriptionOperationStatus(ctx, id)
		if err != nil {
			return false
		}
		view = current
		return terminalSubscriptionStage(view.Stage)
	}, "terminal subscription outcome")
	return view
}

func TestSubscriptionContinueConsentBoundSingleUseAndNoRenewal(t *testing.T) {
	d, _, p, ctx := subscriptionWorkerFixture(t)
	d.subscriptionOps.accepted = subscriptionAcceptedInfo{Protected: true}
	var calls atomic.Int64
	var spec maintenance.LeaseSpec
	entered := make(chan struct{})
	d.subscriptionOps.fetch = func(ctx context.Context, _ string, opts subscription.FetchOptions) ([]byte, http.Header, error) {
		calls.Add(1)
		spec = opts.Network.(*maintenance.Lease).Spec()
		close(entered)
		<-ctx.Done()
		return nil, nil, ctx.Err()
	}
	start, err := d.startSubscriptionOperation(ctx, SubscriptionStart{Kind: "refresh", ProfileID: p.ID})
	if err != nil {
		t.Fatal(err)
	}
	consent, err := d.continueSubscriptionOperation(ctx, start.OperationID, "default", "")
	if err != nil || consent.Stage != "consent_required" || consent.ChallengeID == "" || calls.Load() != 0 {
		t.Fatal(consent, err)
	}
	again, err := d.continueSubscriptionOperation(ctx, start.OperationID, "default", "")
	if err != nil || again.ChallengeID != consent.ChallengeID || again.ConsentExpiresAt != consent.ConsentExpiresAt {
		t.Fatal("consent renewed", again, err)
	}
	other := subscriptionTestSession(t, d)
	if _, err = d.continueSubscriptionOperation(other, start.OperationID, "direct_once", consent.ChallengeID); subscriptionErrorCode(err) != "wrong_session" {
		t.Fatal(err)
	}
	running, err := d.continueSubscriptionOperation(ctx, start.OperationID, "direct_once", consent.ChallengeID)
	if err != nil || running.Stage != "running" {
		t.Fatal(running, err)
	}
	<-entered
	if spec.Egress != maintenance.DirectOnce || spec.SessionID == "" || time.Until(spec.Deadline) > 45*time.Second {
		t.Fatal("wrong lease scope")
	}
	repeated, err := d.continueSubscriptionOperation(ctx, start.OperationID, "default", "")
	if err != nil || repeated.Code != "already_started" || calls.Load() != 1 {
		t.Fatal(repeated, err)
	}
	cancelled, err := d.cancelSubscriptionOperation(ctx, start.OperationID)
	if err != nil || cancelled.Stage != "cancelled" {
		t.Fatal(cancelled, err)
	}
	replayed, err := d.continueSubscriptionOperation(ctx, start.OperationID, "direct_once", consent.ChallengeID)
	if err != nil || replayed.Stage != "cancelled" || calls.Load() != 1 {
		t.Fatal("consent replay started another worker", replayed, err)
	}
}

func TestSubscriptionConsentRejectsChangedProtectionAndHTTPPermission(t *testing.T) {
	d, _, p, ctx := subscriptionWorkerFixture(t)
	d.subscriptionOps.accepted = subscriptionAcceptedInfo{Protected: true}
	var calls atomic.Int64
	d.subscriptionOps.fetch = func(context.Context, string, subscription.FetchOptions) ([]byte, http.Header, error) {
		calls.Add(1)
		return nil, nil, errors.New("must not run")
	}
	v, err := d.startSubscriptionOperation(ctx, SubscriptionStart{Kind: "refresh", ProfileID: p.ID})
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := d.continueSubscriptionOperation(ctx, v.OperationID, "default", "")
	if err != nil {
		t.Fatal(err)
	}
	d.subscriptionOps.mu.Lock()
	d.subscriptionOps.protectionEpoch++
	d.subscriptionOps.mu.Unlock()
	changed, err := d.continueSubscriptionOperation(ctx, v.OperationID, "direct_once", challenge.ChallengeID)
	if err != nil || changed.Stage != "failed" || changed.Code != "scope_changed" || calls.Load() != 0 {
		t.Fatal(changed, err)
	}
	httpOp, err := d.startSubscriptionOperation(ctx, SubscriptionStart{Kind: "import", URL: "http://provider.test/sub/SYNTHETIC_ONLY"})
	if err != nil {
		t.Fatal(err)
	}
	denied, err := d.continueSubscriptionOperation(ctx, httpOp.OperationID, "direct_once", "unrelated-consent")
	if err != nil || denied.Code != "http_opt_in_required" || calls.Load() != 0 {
		t.Fatal(denied, err)
	}
}

func TestSubscriptionRefreshCommitPreservesUnrelatedRepositoryChanges(t *testing.T) {
	d, store, p, ctx := subscriptionWorkerFixture(t)
	p2 := addCanonicalProfile(t, store)
	_, err := d.CommitCanonical(context.Background(), state.AnyRevision, func(s *state.Snapshot) error {
		v := s.Profiles[p.ID]
		v.TrafficUsed = 123
		v.TrafficTotal = 456
		s.Profiles[p.ID] = v
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	d.subscriptionOps.fetch = func(ctx context.Context, _ string, opts subscription.FetchOptions) ([]byte, http.Header, error) {
		if opts.Network == nil {
			t.Error("worker omitted lease")
		}
		close(entered)
		select {
		case <-release:
			return syntheticOperationBody, http.Header{}, nil
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
	}
	running := startDefaultSubscription(t, d, ctx, p.ID)
	<-entered
	_, err = d.CommitCanonical(context.Background(), state.AnyRevision, func(s *state.Snapshot) error {
		on := true
		s.Preferences.CrashReports = &on
		other := s.Profiles[p2.ID]
		other.Name = "Unrelated rename"
		s.Profiles[p2.ID] = other
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	unblock()
	finished := waitSubscriptionTerminal(t, d, ctx, running.OperationID)
	if finished.Stage != "succeeded" {
		t.Fatal(finished)
	}
	after := store.View()
	if after.Preferences.CrashReports == nil || !*after.Preferences.CrashReports || after.Profiles[p2.ID].Name != "Unrelated rename" {
		t.Fatal("refresh overwrote unrelated commit")
	}
	if after.Profiles[p.ID].TrafficUsed != 123 || after.Profiles[p.ID].TrafficTotal != 456 {
		t.Fatal("absent quota header erased previous quota")
	}
}

func TestSubscriptionCancelAckPreventsLateCandidateCommit(t *testing.T) {
	d, store, p, ctx := subscriptionWorkerFixture(t)
	entered := make(chan struct{})
	d.subscriptionOps.fetch = func(ctx context.Context, _ string, _ subscription.FetchOptions) ([]byte, http.Header, error) {
		close(entered)
		<-ctx.Done()
		// A transport may return a buffered success after cancellation.
		return syntheticOperationBody, http.Header{}, nil
	}
	before := store.View()
	running := startDefaultSubscription(t, d, ctx, p.ID)
	<-entered
	cancelled, err := d.cancelSubscriptionOperation(ctx, running.OperationID)
	if err != nil || cancelled.Stage != "cancelled" {
		t.Fatal(cancelled, err)
	}
	if !reflect.DeepEqual(before, store.View()) {
		t.Fatal("candidate committed after cancellation acknowledgement")
	}
	d.subscriptionOps.mu.Lock()
	operation := d.subscriptionOps.operations[running.OperationID]
	d.subscriptionOps.mu.Unlock()
	called := false
	if err := operation.lease.WithCommitFence(context.Background(), func() error { called = true; return nil }); err == nil || called {
		t.Fatal("cancelled lease admitted a later commit")
	}
}

func TestSubscriptionCancelRemainsPendingUntilAdmittedFenceOutcome(t *testing.T) {
	d, store, p, ctx := subscriptionWorkerFixture(t)
	fetching := make(chan struct{})
	d.subscriptionOps.fetch = func(ctx context.Context, _ string, _ subscription.FetchOptions) ([]byte, http.Header, error) {
		close(fetching)
		<-ctx.Done()
		return nil, nil, ctx.Err()
	}
	running := startDefaultSubscription(t, d, ctx, p.ID)
	<-fetching
	d.subscriptionOps.mu.Lock()
	op := d.subscriptionOps.operations[running.OperationID]
	d.subscriptionOps.mu.Unlock()
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	before := store.View()
	nextFlag := before.Preferences.CrashReports == nil || !*before.Preferences.CrashReports
	go func() {
		done <- op.lease.WithCommitFence(context.Background(), func() error {
			close(entered)
			<-release
			_, err := store.Transact(context.Background(), state.AnyRevision, func(s *state.Snapshot) error { s.Preferences.CrashReports = &nextFlag; return nil })
			if err == nil {
				op.outcome.Store(&subscriptionCommitOutcome{stage: "succeeded", profile: p.ID, committed: true})
			}
			return err
		})
	}()
	<-entered
	pending, err := d.cancelSubscriptionOperation(ctx, running.OperationID)
	if err != nil || pending.Stage != "cancelling" || pending.Code != "commit_pending" {
		unblock()
		<-done
		t.Fatal("false cancel acknowledgement while F admitted", pending, err)
	}
	if !reflect.DeepEqual(before, store.View()) {
		t.Fatal("held callback unexpectedly committed")
	}
	unblock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	terminal := waitSubscriptionTerminal(t, d, ctx, running.OperationID)
	if terminal.Stage != "succeeded" {
		t.Fatal("admitted durable write lost its atomic outcome", terminal)
	}
	again, err := d.cancelSubscriptionOperation(ctx, running.OperationID)
	if err != nil || again.Code != "already_completed" {
		t.Fatal(again, err)
	}
}

func TestSubscriptionTargetRemovalCannotBeResurrected(t *testing.T) {
	d, store, p, ctx := subscriptionWorkerFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	d.subscriptionOps.fetch = func(ctx context.Context, _ string, _ subscription.FetchOptions) ([]byte, http.Header, error) {
		close(entered)
		select {
		case <-release:
			return syntheticOperationBody, http.Header{}, nil
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
	}
	running := startDefaultSubscription(t, d, ctx, p.ID)
	<-entered
	if err := store.Profiles().Remove(p.ID); err != nil {
		t.Fatal(err)
	}
	unblock()
	terminal := waitSubscriptionTerminal(t, d, ctx, running.OperationID)
	if terminal.Stage != "failed" || terminal.Code != "profile_stale" {
		t.Fatal(terminal)
	}
	if _, ok := store.View().Profiles[p.ID]; ok {
		t.Fatal("refresh resurrected removed profile")
	}
	data, _ := json.Marshal(terminal)
	if stringsContainsPrivate(data) {
		t.Fatal("terminal error exposed source")
	}
}

func stringsContainsPrivate(data []byte) bool {
	return strings.Contains(string(data), "SYNTHETIC_ONLY") || strings.Contains(string(data), "provider.test")
}

func TestSubscriptionWorkerPanicClosesAdmissionWithoutExposingValue(t *testing.T) {
	d, store, p, ctx := subscriptionWorkerFixture(t)
	before := store.View()
	d.subscriptionOps.fetch = func(context.Context, string, subscription.FetchOptions) ([]byte, http.Header, error) {
		panic("https://provider.test/sub/SYNTHETIC_ONLY")
	}
	running := startDefaultSubscription(t, d, ctx, p.ID)
	terminal := waitSubscriptionTerminal(t, d, ctx, running.OperationID)
	if terminal.Stage != "failed" || terminal.Code != "storage_error" || store.Ready() == nil {
		t.Fatal("unknown worker outcome did not close admission", terminal)
	}
	if !reflect.DeepEqual(before, store.View()) {
		t.Fatal("panic changed canonical data")
	}
	data, _ := json.Marshal(terminal)
	if stringsContainsPrivate(data) {
		t.Fatal("panic input reached wire")
	}
}

func TestSubscriptionEntitlementSharesLeaseAndCancellationPreservesAllData(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		name := "success"
		if cancelled {
			name = "cancelled"
		}
		t.Run(name, func(t *testing.T) {
			d, store, p, ctx := subscriptionWorkerFixture(t)
			managedURL := "https://vpsxd.pro/sub/vpn_1_01234567/0123456789abcdef0123456789abcdef"
			_, err := d.CommitCanonical(context.Background(), state.AnyRevision, func(s *state.Snapshot) error {
				v := s.Profiles[p.ID]
				v.URL = managedURL
				v.Managed = true
				v.Tier = subscription.TierPremium
				v.TrafficUsed, v.TrafficTotal = 7, 50
				s.Profiles[p.ID] = v
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			before := store.View()
			var fetchNetwork subscription.Network
			entered := make(chan struct{})
			d.subscriptionOps.fetch = func(ctx context.Context, _ string, opts subscription.FetchOptions) ([]byte, http.Header, error) {
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > 30*time.Second {
					t.Error("fetch exceeded its total budget")
				}
				fetchNetwork = opts.Network
				if _, err := opts.Network.LookupIP(ctx, "vpsxd.pro"); err != nil {
					t.Error(err)
				}
				return syntheticOperationBody, http.Header{"Subscription-Userinfo": []string{"upload=1; download=2; total=100"}}, nil
			}
			d.subscriptionOps.entitlement = func(ctx context.Context, _, _ string, opts subscription.FetchOptions) (subscription.Entitlement, error) {
				if opts.Network != fetchNetwork {
					t.Error("entitlement escaped operation authority")
				}
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > 15*time.Second || deadline.After(opts.Network.(*maintenance.Lease).Spec().Deadline) {
					t.Error("entitlement exceeded remaining operation budget")
				}
				close(entered)
				if cancelled {
					<-ctx.Done()
				}
				return subscription.Entitlement{Tier: subscription.TierPremium, Active: true}, nil
			}
			running := startDefaultSubscription(t, d, ctx, p.ID)
			<-entered
			if cancelled {
				view, err := d.cancelSubscriptionOperation(ctx, running.OperationID)
				if err != nil || view.Stage != "cancelled" || !reflect.DeepEqual(before, store.View()) {
					t.Fatal("late premium changed cancelled candidate", view, err)
				}
			} else {
				view := waitSubscriptionTerminal(t, d, ctx, running.OperationID)
				after := store.View().Profiles[p.ID]
				if view.Stage != "succeeded" || !after.Managed || after.Tier != subscription.TierPremium || after.TrafficUsed != 3 || after.TrafficTotal != 100 {
					t.Fatal(view)
				}
			}
		})
	}
}
