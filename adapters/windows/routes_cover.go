package windows

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
// The accumulation lives in its own file, free of any OS import, so the rule can
// be tested on every platform the CI builds rather than only where a real route
// table exists.

// routeCover accumulates, for one interface and one address family, the
// default-shaped prefixes that interface has claimed, plus the best metric among
// them.
type routeCover struct {
	full   bool // 0.0.0.0/0 or ::/0
	lower  bool // the /1 below the midpoint: 0.0.0.0/1, ::/1
	upper  bool // the /1 above it: 128.0.0.0/1, 8000::/1
	metric uint32
	seen   bool
}

// covers reports whether the accumulated prefixes reach every address in the
// family: a default route, or both halves of the split pair.
//
// One half on its own is not enough. It routes half the address space and leaves
// the other half where it was, which is a scoped tunnel — the case the guard
// deliberately lets through, since refusing to connect over a tunnel that cannot
// swallow our packets is a false alarm the user cannot act on.
func (c routeCover) covers() bool { return c.full || (c.lower && c.upper) }

// coverKey identifies one accumulator. The address family is part of it so a
// half claimed over IPv4 and a half claimed over IPv6 never add up to coverage
// of either.
type coverKey struct {
	index  uint32
	family uint16 // whatever the platform calls IPv4/IPv6; only used to keep them apart
}

// coverTable accumulates route coverage across a whole route table.
type coverTable map[coverKey]*routeCover

// add records one route: the interface it belongs to, its address family, the
// destination network address, the prefix length, and the metric by which the
// stack ranks it.
//
// Prefixes other than /0 and the two halves cannot cover the address space alone
// or together, so they are dropped here rather than stored and filtered later.
func (t coverTable) add(index uint32, family uint16, dest []byte, ones int, metric uint32) {
	if len(dest) == 0 || ones > 1 || ones < 0 || !isNetworkAddress(dest, ones) {
		return
	}
	key := coverKey{index: index, family: family}
	c := t[key]
	if c == nil {
		c = &routeCover{}
		t[key] = c
	}
	switch {
	case ones == 0:
		c.full = true
	case dest[0]&0x80 == 0:
		c.lower = true
	default:
		c.upper = true
	}
	if !c.seen || metric < c.metric {
		c.metric, c.seen = metric, true
	}
}

// defaults returns, per interface index, the metric of the best route by which
// that interface can capture arbitrary traffic. An interface absent from the map
// has no such route.
//
// An interface that covers both families is reported at its better metric: that
// is the one the stack compares against the other interfaces when it picks a
// path, and the one the guard's "parked at a losing metric" test needs.
func (t coverTable) defaults() map[uint32]uint32 {
	out := make(map[uint32]uint32, 4)
	for key, c := range t {
		if !c.covers() {
			continue
		}
		if cur, ok := out[key.index]; !ok || c.metric < cur {
			out[key.index] = c.metric
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
