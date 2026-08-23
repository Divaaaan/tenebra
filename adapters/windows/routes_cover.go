package windows

import (
	"math/big"
	"sort"
)

// Default-route coverage: what it takes for one interface to be able to capture
// arbitrary traffic, which is the only question the tun-conflict guard asks of a
// route table.
//
// Testing for a literal 0.0.0.0/0 answers it wrong for the most common way a VPN
// takes a machine over. The standard move is not to touch the existing default
// route at all, but to lay the pair 0.0.0.0/1 + 128.0.0.0/1 over it: two routes
// that between them cover every address while each is *more specific* than
// 0.0.0.0/0, so they win regardless of metric and the original default route
// survives underneath for the tunnel's own packets. That is exactly what OpenVPN
// installs for `redirect-gateway def1` and what a Tailscale exit node installs.
// Neither has a zero mask, and one of them has a non-zero destination, so a
// literal-/0 test finds nothing and the guard reports "all clear" for a client
// carrying 100% of the traffic.
//
// The /1 pair is only the common instance of a general shape. Any set of
// prefixes whose ranges tile the whole address space captures everything: /0, the
// /1 pair, the four /2s (0.0.0.0/2 + 64.0.0.0/2 + 128.0.0.0/2 + 192.0.0.0/2), or
// a mix of sizes. Pattern-matching one fixed shape would miss the next one, so
// coverage is decided honestly — by asking whether the interface's routes, taken
// together, leave any address in the family uncovered. A lone half (/1) or
// quarter (/2) is deliberately not coverage: that is a scoped tunnel, which the
// guard lets through, since refusing to connect over a tunnel that cannot swallow
// our packets is a false alarm the user cannot act on.
//
// The accumulation lives in its own file, free of any OS import, so the rule can
// be tested on every platform the CI builds rather than only where a real route
// table exists.

// coverPrefix is one route reduced to what the coverage math needs: how many
// leading bits are fixed, the network address those bits spell, and the effective
// metric the stack ranks the route by.
type coverPrefix struct {
	ones   int
	addr   []byte // network address; 4 bytes for IPv4, 16 for IPv6
	metric uint32
}

// routeCover accumulates, for one interface and one address family, the routes
// that interface has claimed. Whether the set tiles the family, and the metric it
// does so at, are derived from the whole set rather than tracked incrementally:
// coverage is a property no single route carries on its own.
type routeCover struct {
	prefixes []coverPrefix
}

// coverKey identifies one accumulator. The address family is part of it so a
// half claimed over IPv4 and a half claimed over IPv6 never add up to coverage of
// either, and so the two families' metrics — which the stack never ranks against
// each other — stay apart.
type coverKey struct {
	index  uint32
	family uint16 // whatever the platform calls IPv4/IPv6; only used to keep them apart
}

// coverTable accumulates route coverage across a whole route table.
type coverTable map[coverKey]*routeCover

// add records one route: the interface it belongs to, its address family, the
// destination network address, the prefix length, and the effective metric by
// which the stack ranks it.
//
// A route whose destination is not the network address of its own prefix is
// dropped: 1.2.3.4/1 is a route somebody wrote by hand, not half the internet,
// and folding it into coverage would invent a tunnel that is not there. Every
// well-formed prefix length is kept — not only /0 and /1 — because coverage is a
// property of the whole set, and a set of /2s tiles the space no less than the /1
// pair does.
func (t coverTable) add(index uint32, family uint16, dest []byte, ones int, metric uint32) {
	if len(dest) == 0 || ones < 0 || ones > len(dest)*8 || !isNetworkAddress(dest, ones) {
		return
	}
	key := coverKey{index: index, family: family}
	c := t[key]
	if c == nil {
		c = &routeCover{}
		t[key] = c
	}
	// Copy the address: dest points into the OS route-table buffer the caller
	// frees the moment it has walked the rows, and this slice outlives that walk.
	addr := append([]byte(nil), dest...)
	c.prefixes = append(c.prefixes, coverPrefix{ones: ones, addr: addr, metric: metric})
}

// coverMetric reports whether this interface can capture the whole address family
// and, if so, the lowest metric at which it captures all of it: the smallest M
// for which the routes of metric <= M already tile the space.
//
// That threshold, not the best metric of any one route, is what the stack
// effectively ranks the interface by. Taking the minimum metric over every stored
// route would let a /24 LAN route at a wonderful metric masquerade as a
// wonderful-metric default route and drag the figure the guard compares against
// down with it — the physical uplink would then look like a metric-0 path and
// wave every real conflict through. Requiring the whole space to be covered at the
// threshold keeps a specific route from speaking for the default route.
func (c routeCover) coverMetric() (uint32, bool) {
	if len(c.prefixes) == 0 {
		return 0, false
	}
	for _, m := range distinctMetricsAscending(c.prefixes) {
		if coversWithin(c.prefixes, m) {
			return m, true
		}
	}
	return 0, false
}

// distinctMetricsAscending returns the distinct metrics present, sorted, so
// coverMetric can try them best-first and stop at the first that already covers
// everything.
func distinctMetricsAscending(prefixes []coverPrefix) []uint32 {
	seen := make(map[uint32]struct{}, len(prefixes))
	out := make([]uint32, 0, len(prefixes))
	for _, p := range prefixes {
		if _, ok := seen[p.metric]; ok {
			continue
		}
		seen[p.metric] = struct{}{}
		out = append(out, p.metric)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// coversWithin reports whether the prefixes of metric <= maxMetric, taken
// together, cover every address in the family.
//
// Each prefix is the aligned interval [start, start+2^(bits-ones)). Their union
// covers the space iff, walking them by ascending start, none begins past the
// first still-uncovered address: once a gap is left below some start, no later
// prefix — every one of which starts no earlier — can reach back to fill it.
// big.Int carries the arithmetic so the same routine answers for a 32-bit and a
// 128-bit space without special-casing IPv6.
func coversWithin(prefixes []coverPrefix, maxMetric uint32) bool {
	bits := len(prefixes[0].addr) * 8
	total := new(big.Int).Lsh(big.NewInt(1), uint(bits)) // 2^bits

	type span struct{ start, end *big.Int }
	spans := make([]span, 0, len(prefixes))
	for _, p := range prefixes {
		if p.metric > maxMetric {
			continue
		}
		start := new(big.Int).SetBytes(p.addr)
		size := new(big.Int).Lsh(big.NewInt(1), uint(bits-p.ones))
		spans = append(spans, span{start: start, end: new(big.Int).Add(start, size)})
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].start.Cmp(spans[j].start) < 0 })

	covered := big.NewInt(0)
	for _, s := range spans {
		if s.start.Cmp(covered) > 0 {
			return false // a gap below s.start that nothing later can reach
		}
		if s.end.Cmp(covered) > 0 {
			covered = s.end
		}
		if covered.Cmp(total) >= 0 {
			return true
		}
	}
	return covered.Cmp(total) >= 0
}

// defaults returns, per interface index and address family, the metric at which
// that interface can capture the whole of that family. A key absent from the map
// has no such coverage.
//
// Families are kept separate on purpose. The stack ranks IPv4 routes only against
// other IPv4 routes and IPv6 only against IPv6, so collapsing an interface's two
// families into one metric would compare a v4 tunnel against a v6 uplink and call
// a genuine conflict "parked". The caller splits the result back out per family.
func (t coverTable) defaults() map[coverKey]uint32 {
	out := make(map[coverKey]uint32, len(t))
	for key, c := range t {
		if m, ok := c.coverMetric(); ok {
			out[key] = m
		}
	}
	return out
}

// isNetworkAddress reports whether every bit of addr below the first `ones` is
// clear — that addr really is the network address of a /ones prefix, and not a
// route that merely happens to be that many bits long. 1.2.3.4/1 is a route
// somebody wrote by hand, not half the internet.
func isNetworkAddress(addr []byte, ones int) bool {
	for i, b := range addr {
		fixed := ones - i*8
		switch {
		case fixed >= 8:
			continue // byte lies wholly inside the prefix
		case fixed <= 0:
			if b != 0 {
				return false
			}
		default:
			if b&(0xff>>fixed) != 0 {
				return false
			}
		}
	}
	return true
}
