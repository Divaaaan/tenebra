package control

import (
	"crypto/sha256"
	"encoding/hex"
)

// networkFingerprintTag domain-separates the hash, so the token this produces
// cannot collide with, or be mistaken for, any other digest the app stores. The
// version in it is the escape hatch: change what networkIdentity describes and
// bump the tag, and every stored entry stops matching rather than being read
// under a meaning it no longer has.
const networkFingerprintTag = "tenebra-network-v1\x00"

// networkFingerprint names the network this machine is attached to right now, as
// a short opaque token, or "" when the platform cannot tell.
//
// It exists because the strategy that defeats one ISP's DPI is not the strategy
// that defeats another's, and a laptop moves. The bypass strategy was remembered
// in one global setting, so a machine carried from home to a cafe started the
// strategy that won at home — on a network where it may do nothing at all, and
// with routing already sending YouTube and Discord down the direct path on the
// strength of it. Keyed by network, the answer measured here is reused here and
// nowhere else.
//
// Two rules govern what goes in it, and both are about the token never being a
// disclosure:
//
//   - It never leaves the machine. Nothing sends it: it is a map key in a cache
//     file beside the profile store, read at the start of a pick and written at
//     the end of one. The daemon has no telemetry to put it in, and it is not
//     part of the control protocol, the state snapshot, or the diagnostics
//     bundle.
//   - It is hashed anyway. What networkIdentity describes — the default
//     gateway's hardware address and its IP — is a fair description of which
//     network someone is on, and a cache file is a plain-text file a user may
//     well paste into an issue. The digest is what gets written, so the file
//     says "this network, whichever it is" and never says which.
//
// Truncated to eight bytes: this is a cache key over the handful of networks one
// machine sees, where sixty-four bits is far past the point of collisions, and a
// short token keeps the file readable.
func networkFingerprint() string { return fingerprintOf(networkIdentity()) }

// fingerprintOf is the hashing half, split out so it can be tested without a
// network adapter under it. An empty identity — a platform with no
// implementation, or one that could not find a default gateway — stays empty
// rather than becoming the hash of nothing, which every unidentifiable network
// would then share.
func fingerprintOf(identity string) string {
	if identity == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(networkFingerprintTag + identity))
	return hex.EncodeToString(sum[:8])
}
