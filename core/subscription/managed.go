package subscription

import (
	"net/url"
	"regexp"
	"strings"
)

// managedHosts is the allowlist of subscription hosts an entitlement lookup is
// offered for. A subscription whose host is one of these — and whose path has
// the managed shape (see managedPathRe) — is recognised as a managed
// subscription: one served by the operator's own infrastructure, which is what
// lets the client ask that infrastructure for the subscription's entitlement
// tier and surface a badge.
//
// The list is a plain, easily extended allowlist. Recognition gates only
// presentation (a tier lookup and a badge), never any security or connectivity
// property, so a fork can swap in its own hosts — or drop the list entirely —
// and the client behaves identically for every other subscription. A host that
// is not listed is simply treated as an ordinary third-party subscription.
var managedHosts = map[string]bool{
	"vpsxd.pro":        true,
	"vpnxd.pro":        true,
	"chatakfix.online": true,
}

// managedPathRe matches the path shape a managed subscription URL carries:
//
//	/sub/vpn_{numeric-id}_{hash8}/{key32hex}[/{format}]
//
// The single capture group is the 32-hex key that authenticates the entitlement
// lookup. The key is bounded by a following "/" or end-of-path so a longer hex
// run cannot be mistaken for it. A trailing format segment (e.g. /clash) is
// tolerated but ignored.
var managedPathRe = regexp.MustCompile(`^/sub/vpn_\d+_[0-9a-f]{8}/([0-9a-f]{32})(?:/|$)`)

// managed holds the parts of a managed subscription URL the client acts on: the
// origin to reach the entitlement endpoint on (scheme + host[:port], no path),
// and the key that authenticates the lookup.
type managed struct {
	origin string
	key    string
}

// parseManaged reports whether subURL is a managed subscription and, if so,
// returns its origin and key. A subscription is managed when its host is in the
// allowlist and its path matches the managed shape. The check is purely
// syntactic and touches no network; a malformed URL, an unlisted host, or a
// path that does not match yields ok=false.
func parseManaged(subURL string) (managed, bool) {
	u, err := url.Parse(strings.TrimSpace(subURL))
	if err != nil || u.Host == "" {
		return managed{}, false
	}
	if !managedHosts[strings.ToLower(u.Hostname())] {
		return managed{}, false
	}
	m := managedPathRe.FindStringSubmatch(u.Path)
	if m == nil {
		return managed{}, false
	}
	return managed{origin: u.Scheme + "://" + u.Host, key: m[1]}, true
}

// DetectManaged reports whether subURL is a managed subscription — a host in the
// allowlist with the managed path shape. It is the syntactic recognition the
// profile layer records in Profile.Managed.
func DetectManaged(subURL string) bool {
	_, ok := parseManaged(subURL)
	return ok
}

// EntitlementTarget returns the origin and key an entitlement lookup for subURL
// uses. ok is false for any subscription that is not managed, so callers never
// address the entitlement endpoint for a third-party or manual profile. The
// origin is the subscription's own scheme and host, so the lookup always targets
// the same infrastructure the subscription was served from.
func EntitlementTarget(subURL string) (origin, key string, ok bool) {
	m, ok := parseManaged(subURL)
	return m.origin, m.key, ok
}
