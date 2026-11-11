package control

import (
	"context"
	"errors"
	"math"
	"net"
	"net/netip"
	"time"

	"github.com/Divaaaan/tenebra/core/maintenance"
	"github.com/Divaaaan/tenebra/core/model"
	"github.com/Divaaaan/tenebra/core/state"
	"github.com/Divaaaan/tenebra/core/subscription"
)

type subscriptionFetchResult struct {
	Nodes    []model.Node
	Metadata state.SubscriptionMetadata
}
type subscriptionCommitOutcome struct {
	stage, code, profile string
	committed, uncertain bool
}

func bindSubscriptionNetwork(_ context.Context, lease *maintenance.Lease) (func(), error) {
	resolver, err := subscription.NewDoHResolver(subscription.DefaultDoHEndpoints, lease.DialContext)
	if err != nil {
		return nil, err
	}
	if err = lease.BindResolver(resolver.LookupIP); err != nil {
		_ = resolver.Close()
		return nil, err
	}
	return func() { _ = resolver.Close() }, nil
}

func directSubscriptionDial(ctx context.Context, network string, address netip.AddrPort, register func(net.Conn) (net.Conn, error)) (net.Conn, error) {
	// The numeric-only lease resolves through its own bound DoH transport.
	// Register the raw socket before any origin TLS or application bytes.
	raw, err := (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: -1}).DialContext(ctx, network, address.String())
	if err != nil {
		return nil, err
	}
	registered, err := register(raw)
	if err != nil {
		_ = raw.Close()
		return nil, err
	}
	return registered, nil
}

func (d *Daemon) runSubscriptionOperation(operation *subscriptionOperation) {
	owner := d.subscriptionOps
	defer owner.workers.Done()
	defer close(operation.done)
	defer operation.cancel()
	owner.mu.Lock()
	scope := *operation.scope
	acceptance := operation.acceptance
	egress := operation.egress
	ctx := operation.runCtx
	owner.mu.Unlock()
	outcome := &subscriptionCommitOutcome{stage: "failed", code: "network_error"}
	var lease *maintenance.Lease
	var closeNetwork func()
	defer func() {
		panicked := recover() != nil
		if lease != nil {
			lease.Revoke(maintenance.ErrRevoked)
			// The worker retains its slot while an admitted F callback or raw
			// registration is unresolved. Cancel's own two-second wait may time
			// out, but no terminal cancellation is published before this drain.
			_ = lease.WaitDrained(context.Background())
		}
		if closeNetwork != nil {
			closeNetwork()
		}
		if committed := operation.outcome.Load(); committed != nil {
			outcome = committed
		} else if panicked {
			// Never serialize/log a panic value from a credential-bearing path.
			// With no known commit outcome, close canonical admission before
			// reporting storage_error; recovery must establish a trusted state.
			_ = d.canonical.Fence(context.Background())
			outcome = &subscriptionCommitOutcome{stage: "failed", code: "storage_error", uncertain: true}
		} else if ctx.Err() != nil {
			owner.mu.Lock()
			cancelled := operation.view.Stage == "cancelling" || operation.session.closed.Load()
			owner.mu.Unlock()
			if cancelled {
				outcome = &subscriptionCommitOutcome{stage: "cancelled", code: "cancelled"}
			}
		}
		owner.finishWorker(operation, outcome)
	}()
	if ctx.Err() != nil {
		return
	}
	dial := maintenance.NumericDialer(directSubscriptionDial)
	if egress == maintenance.VPNOnly {
		if acceptance.Endpoint == nil || acceptance.Endpoint.RunID() != acceptance.EngineRunID {
			outcome.code = "transport_unavailable"
			return
		}
		dial = acceptance.Endpoint.DialNumeric
	}
	deadline, _ := ctx.Deadline()
	profileID := operation.view.Profile
	if profileID == "" {
		profileID = "import:" + operation.view.OperationID
	}
	spec := maintenance.LeaseSpec{OperationID: operation.view.OperationID, SessionID: operation.session.id,
		ProfileID: profileID, ProfileRevision: scope.target.Revision, EngineRunID: acceptance.EngineRunID,
		AcceptedEpoch: operation.acceptedEpoch, ProtectionEpoch: operation.protectionEpoch, Egress: egress, Deadline: deadline}
	var err error
	lease, err = maintenance.NewLease(ctx, spec, dial)
	if err != nil {
		return
	}
	owner.mu.Lock()
	operation.lease = lease
	owner.mu.Unlock()
	if ctx.Err() != nil {
		return
	}
	closeNetwork, err = owner.network(ctx, lease)
	if err != nil {
		return
	}
	opts := subscription.FetchOptions{AllowHTTP: scope.allowHTTP, Network: lease, Limits: subscription.DefaultLimits()}
	fetchCtx, cancelFetch := context.WithTimeout(ctx, 30*time.Second)
	body, headers, err := owner.fetch(fetchCtx, scope.url, opts)
	cancelFetch()
	if err != nil || ctx.Err() != nil || lease.Err() != nil {
		return
	}
	if len(body) > 8<<20 {
		return
	}
	nodes, _, err := subscription.ParseSubscription(body)
	body = nil
	if err != nil || len(nodes) == 0 || ctx.Err() != nil || lease.Err() != nil {
		return
	}
	metadata := state.SubscriptionMetadata{UpdatedAt: d.now().UTC(), Managed: subscription.DetectManaged(scope.url)}
	if value := headers.Get("Subscription-Userinfo"); value != "" {
		if info, err := subscription.ParseUserInfo(value); err == nil {
			if info.Upload > math.MaxInt64-info.Download {
				return
			}
			usage := &state.SubscriptionUsage{Used: info.Upload + info.Download, Total: info.Total}
			if !info.Expire.IsZero() {
				expiry := info.Expire.UTC()
				usage.ExpiresAt = &expiry
			}
			metadata.Usage = usage
		}
	}
	if metadata.Managed {
		metadata.Tier = subscription.TierFree
		if origin, key, ok := subscription.EntitlementTarget(scope.url); ok {
			entitlementCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			entitlement, err := owner.entitlement(entitlementCtx, origin, key, opts)
			cancel()
			if ctx.Err() != nil || lease.Err() != nil {
				return
			}
			if err == nil {
				metadata.Tier = entitlement.NormalizedTier()
			}
		}
	}
	if ctx.Err() != nil || lease.Err() != nil {
		return
	}
	accepted, err := d.commitSubscriptionRefresh(operation, subscriptionFetchResult{Nodes: nodes, Metadata: metadata})
	if err != nil {
		if operation.outcome.Load() == nil {
			outcome.code = subscriptionCommitCode(err)
			if errors.Is(err, maintenance.ErrRevoked) {
				outcome.code = "policy_denied"
			}
		}
		return
	}
	// Neither F nor S is held while projecting or notifying a control client.
	d.projectRepository(accepted)
	if operation.session.manual {
		d.emitProfiles()
	}
}
