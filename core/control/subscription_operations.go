package control

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Divaaaan/tenebra/core/maintenance"
	"github.com/Divaaaan/tenebra/core/redact"
	"github.com/Divaaaan/tenebra/core/state"
	"github.com/Divaaaan/tenebra/core/subscription"
)

const subscriptionPreparedTTL = 60 * time.Second
const maxSubscriptionTerminalPerSession = 32

type subscriptionScope struct {
	base      state.Snapshot
	target    state.SubscriptionRecord
	url, name string
	allowHTTP bool
}

func (subscriptionScope) String() string { return "<private subscription scope>" }

type subscriptionOperation struct {
	view                           SubscriptionOperationView
	session                        *subscriptionSession
	resource                       string
	scope                          *subscriptionScope
	expires                        time.Time
	timer                          *time.Timer
	started                        bool
	runCtx                         context.Context
	cancel                         context.CancelFunc
	done                           chan struct{}
	lease                          *maintenance.Lease
	egress                         maintenance.Egress
	acceptance                     subscriptionAcceptedInfo
	acceptedEpoch, protectionEpoch uint64
	challenge                      *subscriptionConsentChallenge
	outcome                        atomic.Pointer[subscriptionCommitOutcome]
}

func (*subscriptionOperation) String() string { return "<private subscription operation>" }

// mu (A) owns operation metadata only. It never spans filesystem, network,
// native work, event callbacks, or a cancellation/commit drain.
type subscriptionOperations struct {
	mu                             sync.Mutex
	daemon                         *Daemon
	slots                          subscriptionSlots
	operations                     map[string]*subscriptionOperation
	terminal                       map[string][]string
	closed                         bool
	accepted                       subscriptionAcceptedInfo
	acceptedEpoch, protectionEpoch uint64
	workers                        sync.WaitGroup
	fetch                          func(context.Context, string, subscription.FetchOptions) ([]byte, http.Header, error)
	entitlement                    func(context.Context, string, string, subscription.FetchOptions) (subscription.Entitlement, error)
	network                        func(context.Context, *maintenance.Lease) (func(), error)
}

func newSubscriptionOperations(d *Daemon) *subscriptionOperations {
	return &subscriptionOperations{daemon: d, operations: make(map[string]*subscriptionOperation), terminal: make(map[string][]string),
		accepted:      subscriptionAcceptedInfo{Pending: true},
		acceptedEpoch: 1, protectionEpoch: 1, fetch: subscription.FetchWithOptions,
		entitlement: subscription.FetchEntitlementWithOptions, network: bindSubscriptionNetwork}
}

func subscriptionSource(raw string) (*url.URL, string, error) {
	if raw == "" || len(raw) > 8192 || !utf8.ValidString(raw) || strings.IndexFunc(raw, unicode.IsControl) >= 0 || strings.TrimSpace(raw) != raw {
		return nil, "", subscriptionError("policy_denied")
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Fragment != "" {
		return nil, "", subscriptionError("policy_denied")
	}
	origin, err := subscription.OriginOf(u)
	if err != nil {
		return nil, "", subscriptionError("policy_denied")
	}
	normalized := *u
	normalized.Scheme = origin.Scheme
	normalized.Host = net.JoinHostPort(origin.Host, origin.Port)
	sum := sha256.Sum256([]byte(normalized.String()))
	return u, hex.EncodeToString(sum[:]), nil
}

func (d *Daemon) startSubscriptionOperation(ctx context.Context, input SubscriptionStart) (SubscriptionOperationView, error) {
	session, err := d.subscriptionSession(ctx)
	if err != nil {
		return SubscriptionOperationView{}, err
	}
	if d.canonical == nil || d.canonical.Ready() != nil {
		return SubscriptionOperationView{}, subscriptionError("storage_error")
	}
	base := d.canonical.View()
	scope := &subscriptionScope{base: base}
	var resource, profileID string
	switch input.Kind {
	case "refresh":
		if input.URL != "" || input.Name != "" || input.AllowHTTP {
			return SubscriptionOperationView{}, subscriptionError("policy_denied")
		}
		record, ok := state.SubscriptionAt(base, input.ProfileID)
		if !ok {
			return SubscriptionOperationView{}, subscriptionError("not_found")
		}
		scope.target = record
		scope.url = record.Profile.URL
		scope.allowHTTP = record.HTTPAllowed
		resource = "profile:" + input.ProfileID
		profileID = input.ProfileID
	case "import":
		if input.ProfileID != "" || len(input.Name) > 512 {
			return SubscriptionOperationView{}, subscriptionError("policy_denied")
		}
		scope.url = input.URL
		scope.name = redact.ProfileName(input.Name, input.URL)
		scope.allowHTTP = input.AllowHTTP
	default:
		return SubscriptionOperationView{}, subscriptionError("policy_denied")
	}
	source, digest, err := subscriptionSource(scope.url)
	if err != nil {
		return SubscriptionOperationView{}, err
	}
	if resource == "" {
		resource = "import:" + digest
	}
	now := d.now()
	operation := &subscriptionOperation{view: SubscriptionOperationView{OperationID: rand.Text(), Kind: input.Kind, Stage: "prepared", Profile: profileID},
		session: session, resource: resource, scope: scope, expires: now.Add(subscriptionPreparedTTL)}
	if source.Scheme == "http" && !scope.allowHTTP {
		operation.view.Code = "http_opt_in_required"
	}
	owner := d.subscriptionOps
	if err := owner.slots.reserve(operation.view.OperationID, session.id, resource, session.manual); err != nil {
		return SubscriptionOperationView{}, subscriptionError("busy")
	}
	owner.mu.Lock()
	if owner.closed || session.closed.Load() || session.ctx.Err() != nil {
		owner.mu.Unlock()
		owner.slots.release(operation.view.OperationID)
		return SubscriptionOperationView{}, subscriptionError("wrong_session")
	}
	owner.operations[operation.view.OperationID] = operation
	// Start never acquires a lease or schedules a network worker.
	operation.timer = time.AfterFunc(subscriptionPreparedTTL, func() { owner.expire(operation.view.OperationID) })
	view := operation.view
	owner.mu.Unlock()
	return view, nil
}

func (o *subscriptionOperations) finishLocked(operation *subscriptionOperation, stage, code string) {
	if terminalSubscriptionStage(operation.view.Stage) {
		return
	}
	operation.view.Stage, operation.view.Code = stage, code
	operation.view.ChallengeID, operation.view.ConsentExpiresAt = "", ""
	operation.scope = nil
	if operation.timer != nil {
		operation.timer.Stop()
		operation.timer = nil
	}
	o.slots.release(operation.view.OperationID)
	retained := append(o.terminal[operation.session.id], operation.view.OperationID)
	if len(retained) > maxSubscriptionTerminalPerSession {
		delete(o.operations, retained[0])
		retained = retained[1:]
	}
	o.terminal[operation.session.id] = retained
}

func (o *subscriptionOperations) expire(id string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	operation := o.operations[id]
	if operation != nil && !operation.started && !terminalSubscriptionStage(operation.view.Stage) {
		o.finishLocked(operation, "failed", "expired")
	}
}

func (d *Daemon) subscriptionOperationStatus(ctx context.Context, id string) (SubscriptionOperationView, error) {
	session, err := d.subscriptionSession(ctx)
	if err != nil {
		return SubscriptionOperationView{}, err
	}
	owner := d.subscriptionOps
	owner.mu.Lock()
	defer owner.mu.Unlock()
	operation := owner.operations[id]
	if operation == nil {
		return SubscriptionOperationView{}, subscriptionError("not_found")
	}
	if operation.session != session {
		return SubscriptionOperationView{}, subscriptionError("wrong_session")
	}
	if !operation.started && !terminalSubscriptionStage(operation.view.Stage) && !d.now().Before(operation.expires) {
		owner.finishLocked(operation, "failed", "expired")
	}
	return operation.view, nil
}

func (o *subscriptionOperations) endSession(session *subscriptionSession) {
	if !session.closed.CompareAndSwap(false, true) {
		return
	}
	o.mu.Lock()
	var running []*subscriptionOperation
	for id, operation := range o.operations {
		if operation.session != session {
			continue
		}
		if operation.started && !terminalSubscriptionStage(operation.view.Stage) {
			operation.view.Stage, operation.view.Code = "cancelling", "commit_pending"
			running = append(running, operation)
			continue
		}
		if !terminalSubscriptionStage(operation.view.Stage) {
			o.finishLocked(operation, "cancelled", "cancelled")
		}
		delete(o.operations, id)
	}
	delete(o.terminal, session.id)
	o.mu.Unlock()
	for _, operation := range running {
		o.revoke(operation)
	}
}

func subscriptionErrorCode(err error) string {
	var protocol *subscriptionProtocolError
	if errors.As(err, &protocol) {
		return protocol.code
	}
	switch {
	case errors.Is(err, state.ErrProfileConflict):
		return "profile_stale"
	case errors.Is(err, state.ErrIntentConflict):
		return "intent_stale"
	case errors.Is(err, state.ErrRequiredReference):
		return "required_reference"
	}
	return "storage_error"
}
