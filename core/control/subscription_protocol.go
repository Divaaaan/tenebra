package control

import "context"

// SubscriptionStart is private input to the server-owned operation registry.
// URL and Name are never retained in a terminal or serialized operation view.
type SubscriptionStart struct {
	Kind, ProfileID, URL, Name string
	AllowHTTP                  bool
}

type SubscriptionOperationView struct {
	OperationID      string `json:"operation_id"`
	Kind             string `json:"kind"`
	Stage            string `json:"stage"`
	Code             string `json:"code,omitempty"`
	ChallengeID      string `json:"challenge_id,omitempty"`
	ConsentExpiresAt string `json:"consent_expires_at,omitempty"`
	Profile          string `json:"profile,omitempty"`
}

type subscriptionProtocolError struct{ code string }

func (e *subscriptionProtocolError) Error() string { return e.code }
func subscriptionError(code string) error          { return &subscriptionProtocolError{code: code} }

func terminalSubscriptionStage(stage string) bool {
	return stage == "succeeded" || stage == "failed" || stage == "cancelled"
}

func (d *Daemon) handleSubscriptionOperation(ctx context.Context, req Request) (SubscriptionOperationView, error) {
	switch req.Cmd {
	case CmdStartSubscriptionOperation:
		allowHTTP := req.AllowHTTP != nil && *req.AllowHTTP
		return d.startSubscriptionOperation(ctx, SubscriptionStart{Kind: req.Kind, ProfileID: req.Profile, URL: req.URL, Name: req.Name, AllowHTTP: allowHTTP})
	case CmdContinueSubscriptionOperation:
		return d.continueSubscriptionOperation(ctx, req.OperationID, req.Decision, req.ChallengeID)
	case CmdSubscriptionOperationStatus:
		return d.subscriptionOperationStatus(ctx, req.OperationID)
	case CmdCancelSubscriptionOperation:
		return d.cancelSubscriptionOperation(ctx, req.OperationID)
	default:
		return SubscriptionOperationView{}, subscriptionError("policy_denied")
	}
}
