// Package dnspick chooses a DNS resolver that actually works on the network the
// user is sitting on.
//
// A VPN client normally ships one hardcoded resolver pair and hopes. That fails
// in the two ways this package exists to survive:
//
//   - The ISP blocks the encrypted resolver. Measured on the author's connection
//     on 2026-08-18: DoH to Cloudflare, Google and Yandex times out entirely and
//     Quad9 answers 505, while AdGuard and Comss answer normally. A client
//     pinned to Cloudflare has no working resolver at all on that line, and the
//     symptom is "the VPN connects and nothing loads".
//   - The ISP answers, but lies. Plain DNS on port 53 is rewritten in transit
//     for censored names, so a resolver that responds quickly can still be
//     feeding forged addresses. Speed alone cannot tell the difference.
//
// Hence the ordering rule below: an encrypted resolver that works always beats a
// plain one that works, however much faster the plain one looks — the fast
// answer is fast precisely because the middlebox produced it.
package dnspick

import "sort"

// Kind is how a candidate resolver is reached.
type Kind string

const (
	// KindDoH is DNS-over-HTTPS. Contents are authenticated and opaque to the
	// path, so an on-path censor can block it but cannot forge an answer.
	KindDoH Kind = "doh"
	// KindDoT is DNS-over-TLS. Same guarantee as DoH; a distinct port (853) that
	// some networks block wholesale while leaving 443 alone.
	KindDoT Kind = "dot"
	// KindPlain is unencrypted DNS on port 53: usable as a last resort, and
	// forgeable by anyone on the path.
	KindPlain Kind = "plain"
)

// Encrypted reports whether answers of this kind are protected from tampering.
func (k Kind) Encrypted() bool { return k == KindDoH || k == KindDoT }

// Candidate is one resolver the picker may choose.
type Candidate struct {
	// Name is what the UI shows, e.g. "AdGuard".
	Name string
	// Address is the resolver in sing-box form, e.g. "https://dns.adguard-dns.com/dns-query".
	Address string
	Kind    Kind
}

// Result is one candidate's measured outcome.
type Result struct {
	Candidate Candidate
	// OK is true when the resolver returned a usable answer.
	OK bool
	// RTTMs is the round-trip in milliseconds; meaningful only when OK.
	RTTMs int64
	// Err describes the failure for the log; empty when OK.
	Err string
}

// Rank orders results best-first.
//
// Ordering, in sequence:
//
//  1. Working before broken — an unreachable resolver is not a choice.
//  2. Encrypted before plain. This is the rule that matters and the one a
//     latency-only picker gets wrong: on a filtered line the plain resolver is
//     usually the fastest responder *because* the answer came from a middlebox a
//     few milliseconds away rather than the real server. Preferring it would pick
//     the tamperer every time.
//  3. Lower RTT.
//  4. Input order, so the result is deterministic and the shipped preference
//     (which resolver we would rather send users to) survives ties.
//
// The input slice is not modified.
func Rank(results []Result) []Result {
	out := make([]Result, len(results))
	copy(out, results)

	idx := make(map[string]int, len(results))
	for i, r := range results {
		if _, seen := idx[r.Candidate.Address]; !seen {
			idx[r.Candidate.Address] = i
		}
	}

	sort.SliceStable(out, func(a, b int) bool {
		ra, rb := out[a], out[b]
		if ra.OK != rb.OK {
			return ra.OK
		}
		if ea, eb := ra.Candidate.Kind.Encrypted(), rb.Candidate.Kind.Encrypted(); ea != eb {
			return ea
		}
		if ra.RTTMs != rb.RTTMs {
			return ra.RTTMs < rb.RTTMs
		}
		return idx[ra.Candidate.Address] < idx[rb.Candidate.Address]
	})
	return out
}

// Best returns the resolver to use, and whether one was found.
//
// It returns nothing rather than the least-bad candidate when everything failed:
// silently falling back to a resolver we just measured as broken would produce a
// client that reports success and resolves nothing. The caller should keep its
// current setting and say so.
func Best(results []Result) (Candidate, bool) {
	ranked := Rank(results)
	if len(ranked) == 0 || !ranked[0].OK {
		return Candidate{}, false
	}
	return ranked[0].Candidate, true
}

// DirectCandidates are the resolvers tried for destinations that bypass the
// tunnel. They are ordered by preference for a Russian ISP: the local ones
// answer in single-digit milliseconds and are the least likely to be filtered,
// with the encrypted international ones behind them.
//
// This list is what gets probed, not what gets trusted: whichever survives the
// probe wins, and the ordering only breaks ties.
func DirectCandidates() []Candidate {
	return []Candidate{
		{Name: "AdGuard", Address: "https://dns.adguard-dns.com/dns-query", Kind: KindDoH},
		{Name: "Comss", Address: "https://dns.comss.one/dns-query", Kind: KindDoH},
		{Name: "Yandex", Address: "https://common.dot.dns.yandex.net/dns-query", Kind: KindDoH},
		{Name: "Cloudflare", Address: "https://cloudflare-dns.com/dns-query", Kind: KindDoH},
		{Name: "Google", Address: "https://dns.google/dns-query", Kind: KindDoH},
		{Name: "Quad9", Address: "tls://dns.quad9.net", Kind: KindDoT},
		// Plain entries are last and only chosen when every encrypted option is
		// blocked — better a forgeable resolver than no name resolution at all.
		{Name: "Yandex (plain)", Address: "77.88.8.8", Kind: KindPlain},
		{Name: "Google (plain)", Address: "8.8.8.8", Kind: KindPlain},
	}
}

// RemoteCandidates are the resolvers tried for tunnelled destinations. They are
// reached through the proxy, so ISP filtering does not apply and the choice is
// about latency and reliability of the resolver itself.
func RemoteCandidates() []Candidate {
	return []Candidate{
		{Name: "Cloudflare", Address: "tls://1.1.1.1", Kind: KindDoT},
		{Name: "Google", Address: "https://dns.google/dns-query", Kind: KindDoH},
		{Name: "Quad9", Address: "tls://dns.quad9.net", Kind: KindDoT},
		{Name: "AdGuard", Address: "https://dns.adguard-dns.com/dns-query", Kind: KindDoH},
	}
}
