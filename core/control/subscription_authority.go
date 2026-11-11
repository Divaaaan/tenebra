package control

import (
	"errors"
	"sync"

	"github.com/Divaaaan/tenebra/core/maintenance"
)

// subscriptionAcceptedInfo is published from actual lifecycle acceptance. It is
// never populated from GUI state, a connected preference, or runner availability.
type subscriptionAcceptedInfo struct {
	Connected                                       bool
	Generation                                      uint64
	EngineRunID                                     maintenance.RunID
	ProfileID, NodeID                               string
	RequiredNodeIDs                                 []string
	DesiredConfigRevision, AppliedConfigRevision    uint64
	Pending                                         bool
	Endpoint                                        maintenance.Endpoint
	Protected, ConfirmedUnprotected, ExplicitLegacy bool
}

type subscriptionAdmission string

const (
	subscriptionAdmit       subscriptionAdmission = "admit"
	subscriptionDefer       subscriptionAdmission = "defer"
	subscriptionChallenge   subscriptionAdmission = "challenge"
	subscriptionUnsupported subscriptionAdmission = "unsupported"
)

// classifySubscriptionEgress is a pure admission decision, not permission to
// send traffic. A challenge cannot construct a direct dialer; Continue must
// independently validate and consume session-bound consent first.
func classifySubscriptionEgress(info subscriptionAcceptedInfo, manual bool) (maintenance.Egress, subscriptionAdmission) {
	if info.Pending {
		return "", subscriptionDefer
	}
	if info.Connected {
		if info.Generation == 0 || info.EngineRunID == "" || info.ProfileID == "" || info.NodeID == "" ||
			info.DesiredConfigRevision == 0 || info.DesiredConfigRevision != info.AppliedConfigRevision {
			return "", subscriptionDefer
		}
		if info.Endpoint == nil || info.Endpoint.RunID() != info.EngineRunID {
			if manual {
				return "", subscriptionUnsupported
			}
			return "", subscriptionDefer
		}
		return maintenance.VPNOnly, subscriptionAdmit
	}
	if !info.Protected && info.ConfirmedUnprotected {
		return maintenance.UnprotectedDirect, subscriptionAdmit
	}
	if manual {
		return maintenance.DirectOnce, subscriptionChallenge
	}
	return "", subscriptionDefer
}

var (
	errSubscriptionBusy     = errors.New("subscription_resource_busy")
	errSubscriptionCapacity = errors.New("subscription_operation_limit")
	errSubscriptionSlot     = errors.New("subscription_invalid_operation")
)

const maxSubscriptionManualPerSession = 32
const maxSubscriptionNonterminal = 128

type subscriptionSlot struct {
	operationID, sessionID, resource string
	manual                           bool
}

// All nonterminal states, including consent/deferred/cancelling, retain a slot.
// The registry performs metadata changes only; release follows terminal drain.
type subscriptionSlots struct {
	mu         sync.Mutex
	operations map[string]subscriptionSlot
	resources  map[string]string
	sessions   map[string]int
}

func (s *subscriptionSlots) reserve(operationID, sessionID, resource string, manual bool) error {
	if operationID == "" || len(operationID) > 128 || len(sessionID) > 128 || resource == "" || len(resource) > 256 || (manual && sessionID == "") {
		return errSubscriptionSlot
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.operations == nil {
		s.operations = make(map[string]subscriptionSlot)
		s.resources = make(map[string]string)
		s.sessions = make(map[string]int)
	}
	if _, exists := s.operations[operationID]; exists {
		return errSubscriptionBusy
	}
	if _, exists := s.resources[resource]; exists {
		return errSubscriptionBusy
	}
	if len(s.operations) >= maxSubscriptionNonterminal || (manual && s.sessions[sessionID] >= maxSubscriptionManualPerSession) {
		return errSubscriptionCapacity
	}
	s.operations[operationID] = subscriptionSlot{operationID, sessionID, resource, manual}
	s.resources[resource] = operationID
	if manual {
		s.sessions[sessionID]++
	}
	return nil
}

func (s *subscriptionSlots) release(operationID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	operation, exists := s.operations[operationID]
	if !exists {
		return
	}
	delete(s.operations, operationID)
	delete(s.resources, operation.resource)
	if operation.manual {
		s.sessions[operation.sessionID]--
		if s.sessions[operation.sessionID] == 0 {
			delete(s.sessions, operation.sessionID)
		}
	}
}
