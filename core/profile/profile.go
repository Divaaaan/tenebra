// Package profile defines the profile and server types the UI works with and
// persists them to disk. A profile groups the proxy servers imported from a
// single subscription URL or added by hand; the Store keeps every profile in
// one JSON file and saves it atomically.
package profile

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/Divaaaan/tenebra/core/model"
)

// Source distinguishes how a profile's servers were obtained.
const (
	SourceSubscription = "subscription"
	SourceManual       = "manual"
)

// Server is a proxy node with a stable identifier. model.Node is embedded so
// the JSON flattens to {id, protocol, name, server, port, ...}; the control
// protocol's Node type sees exactly those fields.
type Server struct {
	ID string `json:"id"`
	model.Node
}

// Profile groups servers from one source. The JSON layout matches the Profile
// type in docs/control-protocol.md.
type Profile struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	Source       string     `json:"source"`
	URL          string     `json:"url,omitempty"`
	Servers      []Server   `json:"nodes"`
	UpdatedAt    time.Time  `json:"updatedAt"`
	ExpiresAt    *time.Time `json:"expiresAt,omitempty"`
	TrafficUsed  int64      `json:"trafficUsed,omitempty"`
	TrafficTotal int64      `json:"trafficTotal,omitempty"`
}

// NewProfile builds a profile from a freshly parsed set of nodes, assigning a
// random profile ID and stable per-server IDs. UpdatedAt is set to now.
func NewProfile(name, source, url string, nodes []model.Node) (Profile, error) {
	id, err := newProfileID()
	if err != nil {
		return Profile{}, err
	}
	return Profile{
		ID:        id,
		Name:      name,
		Source:    source,
		URL:       url,
		Servers:   assignServerIDs(nodes),
		UpdatedAt: time.Now().UTC(),
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

// assignServerIDs wraps each node in a Server with a content-derived ID. The ID
// is stable: refreshing a subscription that returns the same server (same
// protocol, address and credentials) yields the same ID, so connection state
// and ping history keyed on it survive the refresh. Changing the auth material
// or endpoint produces a new ID, which is the intended behaviour — it is a
// different server.
func assignServerIDs(nodes []model.Node) []Server {
	if nodes == nil {
		return nil
	}
	servers := make([]Server, len(nodes))
	for i, n := range nodes {
		servers[i] = Server{ID: serverID(n), Node: n}
	}
	return servers
}

// serverID is the first 8 bytes of a SHA-256 over the fields that identify a
// server: protocol, endpoint and the credentials that authenticate to it. The
// name is deliberately excluded so a relabelled node keeps its ID. Fields are
// length-prefixed to avoid collisions between adjacent values.
//
// The common credential fields (UUID/Password/Method) aren't enough for every
// protocol: an AmneziaWG peer carries none of them, so two distinct WG peers on
// the same host:port would collide to one ID; likewise two REALITY variants that
// differ only in public_key/short_id, or two transports that differ only in
// type/path. Folding those discriminators in keeps the ID a faithful identity so
// a refresh maps each server to the right prior entry. Absent sub-structs
// contribute empty fields (length-prefixed), so a plain node's ID is unchanged
// from before only when those fields were already empty.
func serverID(n model.Node) string {
	h := sha256.New()
	writeField(h, string(n.Protocol))
	writeField(h, n.Server)
	writeField(h, fmt.Sprintf("%d", n.Port))
	writeField(h, n.UUID)
	writeField(h, n.Password)
	writeField(h, n.Method)
	if n.WireGuard != nil {
		writeField(h, n.WireGuard.PrivateKey)
		writeField(h, n.WireGuard.PeerPublicKey)
	} else {
		writeField(h, "")
		writeField(h, "")
	}
	if n.TLS != nil && n.TLS.Reality != nil {
		writeField(h, n.TLS.Reality.PublicKey)
		writeField(h, n.TLS.Reality.ShortID)
	} else {
		writeField(h, "")
		writeField(h, "")
	}
	if n.Transport != nil {
		writeField(h, n.Transport.Type)
		writeField(h, n.Transport.Path)
		writeField(h, n.Transport.ServiceName)
	} else {
		writeField(h, "")
		writeField(h, "")
		writeField(h, "")
	}
	sum := h.Sum(nil)
	return hex.EncodeToString(sum[:8])
}

// writeField feeds a length-prefixed field into the hash so that, e.g.,
// ("ab","c") and ("a","bc") hash differently.
func writeField(h interface{ Write([]byte) (int, error) }, s string) {
	var lp [8]byte
	n := len(s)
	for i := 0; i < 8; i++ {
		lp[i] = byte(n)
		n >>= 8
	}
	h.Write(lp[:])
	h.Write([]byte(s))
}
