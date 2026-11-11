// Package profile defines the profile and server types the UI works with and
// persists them to disk. A profile groups the proxy servers imported from a
// single subscription URL or added by hand; the Store keeps every profile in
// one JSON file and saves it atomically.
package profile

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Divaaaan/tenebra/core/model"
)

// Source distinguishes how a profile's servers were obtained.
const (
	SourceSubscription = "subscription"
	SourceManual       = "manual"
)

// Server is a proxy node with a stable identifier. model.Node is embedded so the
// JSON flattens to {id, protocol, name, server, port, ...} — credentials and all.
// That full form is the on-disk persistence shape only; it never travels over the
// control socket. The wire-facing view is the redacted projection in the control
// package (redactedNode), which strips every secret before the UI sees it.
type Server struct {
	ID string `json:"id"`
	model.Node
}

// Profile groups servers from one source. The JSON layout matches the Profile
// type in docs/control-protocol.md.
type Profile struct {
	IdentityVersion  int                        `json:"identityVersion,omitempty"`
	Revision         uint64                     `json:"revision,omitempty"`
	HTTPAllowed      bool                       `json:"httpAllowed,omitempty"`
	AdditionalFields map[string]json.RawMessage `json:"-"`
	ID               string                     `json:"id"`
	Name             string                     `json:"name"`
	Source           string                     `json:"source"`
	URL              string                     `json:"url,omitempty"`
	Servers          []Server                   `json:"nodes"`
	UpdatedAt        time.Time                  `json:"updatedAt"`
	ExpiresAt        *time.Time                 `json:"expiresAt,omitempty"`
	TrafficUsed      int64                      `json:"trafficUsed,omitempty"`
	TrafficTotal     int64                      `json:"trafficTotal,omitempty"`
	// Managed marks a subscription recognised as one served by the operator's own
	// infrastructure — its host and path shape match the managed-subscription
	// form (see subscription.DetectManaged). It gates only presentation: an
	// entitlement lookup against the same origin, and a badge. It is never a
	// security property, so a manual profile, a third-party subscription, or a
	// fork simply leaves it false.
	Managed bool `json:"managed,omitempty"`
	// Tier is the entitlement tier last resolved for a managed subscription:
	// "premium", "free", or "" when unknown or not applicable. It exists purely
	// for the UI (a badge). Any real premium capability is derived from data the
	// operator's server returns, not from this flag, so a client that forces it
	// to "premium" gains only the label.
	Tier string `json:"tier,omitempty"`
}

// NewProfile builds a profile from a freshly parsed set of nodes, assigning a
// random profile ID and independent opaque per-server IDs. UpdatedAt is set to now.
func NewProfile(name, source, url string, nodes []model.Node) (Profile, error) {
	id, err := newProfileID()
	if err != nil {
		return Profile{}, err
	}
	servers, err := ReconcileServers(nil, nodes, nil)
	if err != nil {
		return Profile{}, err
	}
	return Profile{
		IdentityVersion: 2,
		ID:              id,
		Name:            name,
		Source:          source,
		URL:             url,
		Servers:         servers,
		UpdatedAt:       time.Now().UTC(),
	}, nil
}

// newProfileID returns a random 16-hex-character identifier.
func newProfileID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate profile id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}
