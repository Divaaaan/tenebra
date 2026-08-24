package zapret

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// excludeFile is the bundle's user-editable exclusion list. Every shipped
// strategy passes it to winws as --ipset-exclude, so an address listed here is
// left alone by the packet filter.
const excludeFile = "ipset-exclude-user.txt"

// ExcludeHeader marks the block this package owns inside that file, so a user's
// own entries above it survive being rewritten.
const excludeHeader = "# --- tenebra: VPN node addresses, managed automatically ---"

// nodeCacheFile remembers the last address each node name answered with. It
// lives beside the exclusion list and is carried across bundle updates with it
// (see userFiles).
const nodeCacheFile = "tenebra-node-addresses.json"

// DefaultResolveBudget caps the wall clock one Exclude call may spend waiting
// for names it has no address for. Two seconds buys a full round trip to an
// upstream resolver and a retry, and is short enough to read as part of
// connecting rather than a hang.
const DefaultResolveBudget = 2 * time.Second

// DefaultRetryAfter is how long a name that came back with nothing is left out
// of the blocking wait. It is still looked up on every call — the answer just
// arrives too late for that call and lands in the cache for the next one.
//
// Without it a name that is permanently dead (a node decommissioned upstream,
// still sitting in the user's profile) costs the full budget on every single
// connect, forever, for an answer that has not changed in weeks.
const DefaultRetryAfter = 5 * time.Minute

// resolveParallel bounds the lookups in flight. A subscription can carry a
// hundred nodes: one after another, the first slow name eats the whole budget,
// while all hundred at once is a burst a local resolver answers with drops.
const resolveParallel = 16

// cacheTTL is how long a remembered address is worth writing. Long, because the
// cost of a stale entry is small — an address the filter leaves alone that no
// longer hosts a node — while the cost of forgetting one is the failure this
// whole file exists to prevent, at the worst possible moment.
const cacheTTL = 30 * 24 * time.Hour

// maxCacheEntries bounds the file. Profiles come and go; without a cap the
// remembered set would only ever grow.
const maxCacheEntries = 256

// staleAfter is how long a remembered address may go unconfirmed before writing
// it is worth a word. Under it, a name that did not answer in this call is
// ordinary rather than interesting: the refresh is in flight and the address is
// minutes old. Over it, DNS has not agreed with this address for a while, and if
// the node has moved that is the only clue anyone gets.
const staleAfter = time.Hour

// Lookup resolves a hostname to addresses. Callers pass the resolvers they want
// the node names asked through — see Excluder.Exclude for why more than one.
type Lookup func(ctx context.Context, host string) ([]net.IP, error)

// SystemLookup resolves through the machine's own resolver.
func SystemLookup(ctx context.Context, host string) ([]net.IP, error) {
	return net.DefaultResolver.LookupIP(ctx, "ip", host)
}

// ExcludeReport says what the exclusion list ended up covering.
type ExcludeReport struct {
	// Unresolved are the node hostnames with no address at all: nothing fresh,
	// nothing remembered. These nodes are still in the packet filter's way.
	Unresolved []string
	// FromCache are the names covered by an address DNS has not confirmed for a
	// while (see staleAfter). Protected, but on an old answer — worth a debug
	// line, not a warning. A name whose refresh simply has not landed yet is not
	// in here: that is every name on every connect and says nothing.
	FromCache []string
}

// Excluder writes exit-node addresses into a bundle's exclusion list and
// remembers what it learned, so one bad DNS moment does not undo it.
//
// It holds the remembered addresses, so a caller keeps one for the life of the
// process rather than building one per call.
type Excluder struct {
	// Budget caps the wall clock one call may spend waiting for names it has no
	// address for. Zero means DefaultResolveBudget.
	Budget time.Duration
	// RetryAfter is how long a name that answered with nothing stays out of the
	// blocking wait. Zero means DefaultRetryAfter.
	RetryAfter time.Duration

	mu     sync.Mutex
	cache  map[string]cacheEntry
	loaded bool
}

// cacheEntry is one name's last known state.
type cacheEntry struct {
	// Addrs is the last answer this name gave, whenever that was.
	Addrs []string `json:"addrs,omitempty"`
	// Seen is when Addrs was confirmed.
	Seen time.Time `json:"seen,omitempty"`
	// Failed is when the last attempt came back with nothing usable. Cleared by
	// the next answer.
	Failed time.Time `json:"failed,omitempty"`
}

// cacheFile is the on-disk shape of the remembered set.
type cacheFile struct {
	Version int                   `json:"version"`
	Hosts   map[string]cacheEntry `json:"hosts"`
}

// Exclude writes the exit-node addresses into the bundle's exclusion list,
// preserving any lines the user put there themselves, and reports what it could
// not cover.
//
// Why the bypass must not touch the tunnel's own traffic: winws attaches to
// every interface and filters by port, and the ports it watches (443, 8443,
// 2053, 2083, 2087, 2096) are exactly the ports exit nodes are commonly served
// on. A VLESS-Reality handshake carries a TLS ClientHello like any other, so a
// desync aimed at the censor can land on the connection to the node instead —
// the observed shape is a TCP port that opens and then goes silent, which reads
// as "the node is down" and sends the user hunting through their subscription
// for a fault that is on their own machine.
//
// Nodes given by name are resolved here and their addresses written. The file is
// an ipset, not a hostlist: winws documents --ipset-exclude as "one ip/CIDR per
// line, ipv4 and ipv6 accepted" and answers anything else with "bad ip or
// subnet", so a hostname put in this file is a line thrown away, not a lookup
// deferred to winws. Subscriptions that address their nodes by name are ordinary,
// and every one of them used to drop out of this list without a word.
//
// The lookups are the caller's to choose, and the reason there is a list of them
// is that the address this function must exclude is not "the address of the
// name" but "the address sing-box will dial". Those differ whenever the resolver
// asked differs: round-robin and geo-DNS hand different callers different
// records, and a censor that poisons the name — often the very reason a node is
// hidden behind one — poisons the machine's resolver while an encrypted resolver
// answers honestly. Excluding an address the tunnel does not use costs nothing
// (the filter leaves one more address alone); missing the one it does use costs
// the whole point. So every resolver's answer is written, and the caller is
// expected to pass both the system resolver and the one sing-box is configured
// to resolve node names through.
//
// This must happen before winws starts, not alongside it: winws reads its lists
// once at startup, so an exclusion written after it is launched has no effect
// until the next restart, and the tunnel comes up in between. That is why the
// call is on the connect path and why it is bounded — see the budget and the
// remembered addresses, which between them mean a slow or dead resolver costs a
// short pause on the first connect and nothing after it.
func (e *Excluder) Exclude(dir string, servers []string, lookups []Lookup) (ExcludeReport, error) {
	e.load(dir)

	ips, hosts := splitServers(servers)
	resolved, report := e.resolve(hosts, lookups)
	for _, addrs := range resolved {
		ips = append(ips, addrs...)
	}
	sort.Strings(ips)
	ips = dedupe(ips)

	path := filepath.Join(dir, "lists", excludeFile)
	var b strings.Builder
	for _, l := range userLines(path) {
		b.WriteString(l)
		b.WriteString("\r\n")
	}
	b.WriteString(excludeHeader)
	b.WriteString("\r\n")
	for _, ip := range ips {
		b.WriteString(ip)
		b.WriteString("\r\n")
	}
	// The bundle's own comment warns against leaving these files empty; a lone
	// header keeps the file non-empty even when no node yielded an address.

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return report, fmt.Errorf("zapret: не создать каталог списков: %w", err)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return report, fmt.Errorf("zapret: не записать %s: %w", excludeFile, err)
	}
	e.save(dir)
	return report, nil
}

// splitServers turns the configured node endpoints into the address literals an
// ipset accepts and the hostnames still to be resolved. Names are lower-cased
// and deduplicated: DNS is case-insensitive, so two spellings are one lookup.
func splitServers(servers []string) (ips []string, hosts []string) {
	seen := make(map[string]struct{})
	for _, s := range servers {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		// Bracketed IPv6 ("[2001:db8::1]") arrives from URL-shaped sources.
		s = strings.TrimSuffix(strings.TrimPrefix(s, "["), "]")
		if net.ParseIP(s) != nil {
			ips = append(ips, s)
			continue
		}
		host := strings.ToLower(s)
		if _, dup := seen[host]; dup {
			continue
		}
		seen[host] = struct{}{}
		hosts = append(hosts, host)
	}
	return ips, hosts
}

// resolve looks the names up through every resolver and returns the addresses to
// write for each, which is the fresh answer when there is one and the last known
// answer when there is not.
//
// What it waits for is the point. Every lookup is fired at once; the wait covers
// only the names this call has nothing to write for, and ends the moment those
// answer. A name already covered by a remembered address does not hold the
// connect back: its lookup keeps running into the cache and lands in the file on
// the next call. Neither does a name that came back with nothing very recently —
// asking again is worth a background attempt, not another full budget on every
// connect.
func (e *Excluder) resolve(hosts []string, lookups []Lookup) (map[string][]string, ExcludeReport) {
	// A nil in the slice would be a nil call inside a goroutine, which is a panic
	// no recover can catch and a daemon that takes the user's tunnel down with it.
	// Callers build this list conditionally; drop the gaps rather than trust them.
	usable := lookups[:0:0]
	for _, l := range lookups {
		if l != nil {
			usable = append(usable, l)
		}
	}
	lookups = usable

	if len(hosts) == 0 || len(lookups) == 0 {
		if len(hosts) > 0 {
			// No resolver at all: nothing can be learned, but what was learned
			// before is still worth writing.
			return e.fromCacheOnly(hosts)
		}
		return nil, ExcludeReport{}
	}

	budget := e.Budget
	if budget <= 0 {
		budget = DefaultResolveBudget
	}
	ctx, cancel := context.WithTimeout(context.Background(), budget)

	fresh := make(map[string]map[string]struct{}, len(hosts))
	var mu sync.Mutex
	var all, blocking sync.WaitGroup
	slots := make(chan struct{}, resolveParallel)

	launch := func(host string, holding bool) {
		if holding {
			blocking.Add(len(lookups))
		}
		for _, lookup := range lookups {
			all.Add(1)
			go func(lookup Lookup) {
				defer all.Done()
				if holding {
					defer blocking.Done()
				}
				slots <- struct{}{}
				defer func() { <-slots }()

				// A lookup that starts after the deadline returns immediately with
				// the context's error, so a long queue drains at the budget instead
				// of running past it.
				addrs, err := lookup(ctx, host)
				if err != nil {
					return
				}
				var usable []string
				for _, ip := range addrs {
					if !usableNodeIP(ip) {
						continue
					}
					usable = append(usable, ip.String())
				}
				if len(usable) == 0 {
					return
				}
				mu.Lock()
				set := fresh[host]
				if set == nil {
					set = make(map[string]struct{}, len(usable))
					fresh[host] = set
				}
				for _, a := range usable {
					set[a] = struct{}{}
				}
				merged := sortedKeys(set)
				mu.Unlock()
				// Remember it right away rather than at the end of the call: this
				// goroutine may well outlive the call that started it, and its
				// answer is exactly what the next one should not have to wait for.
				e.remember(host, merged)
			}(lookup)
		}
	}

	// The names the call has to wait for go first, so on a warm cache one new
	// name is not queued behind a hundred refreshes it does not depend on.
	var refreshing []string
	for _, h := range hosts {
		if e.holdsTheWait(h) {
			launch(h, true)
			continue
		}
		refreshing = append(refreshing, h)
	}
	for _, h := range refreshing {
		launch(h, false)
	}

	// The stragglers are wanted, so the deadline is what ends them, not this
	// function returning.
	go func() {
		all.Wait()
		cancel()
	}()

	settled := make(chan struct{})
	go func() {
		blocking.Wait()
		close(settled)
	}()
	select {
	case <-settled:
	case <-ctx.Done():
	}

	mu.Lock()
	out := make(map[string][]string, len(hosts))
	for host, set := range fresh {
		out[host] = sortedKeys(set)
	}
	mu.Unlock()

	var report ExcludeReport
	for _, h := range hosts {
		if len(out[h]) > 0 {
			continue
		}
		// Nothing fresh for this name. Record the miss — it is what keeps the next
		// connect from spending the budget on it again — and fall back to what the
		// name last answered with.
		if addrs, stale := e.missed(h); len(addrs) > 0 {
			out[h] = addrs
			if stale {
				report.FromCache = append(report.FromCache, h)
			}
			continue
		}
		report.Unresolved = append(report.Unresolved, h)
	}
	sort.Strings(report.FromCache)
	sort.Strings(report.Unresolved)
	return out, report
}

// fromCacheOnly is the answer when there is no resolver to ask: everything falls
// back to what was remembered.
func (e *Excluder) fromCacheOnly(hosts []string) (map[string][]string, ExcludeReport) {
	out := make(map[string][]string, len(hosts))
	var report ExcludeReport
	for _, h := range hosts {
		if addrs, stale := e.missed(h); len(addrs) > 0 {
			out[h] = addrs
			if stale {
				report.FromCache = append(report.FromCache, h)
			}
			continue
		}
		report.Unresolved = append(report.Unresolved, h)
	}
	sort.Strings(report.FromCache)
	sort.Strings(report.Unresolved)
	return out, report
}

// holdsTheWait reports whether this name is one the caller must wait for: it has
// no remembered address, and it did not just fail.
func (e *Excluder) holdsTheWait(host string) bool {
	retry := e.RetryAfter
	if retry <= 0 {
		retry = DefaultRetryAfter
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	entry, ok := e.cache[host]
	if !ok {
		return true
	}
	if len(entry.Addrs) > 0 && time.Since(entry.Seen) < cacheTTL {
		return false
	}
	return time.Since(entry.Failed) >= retry
}

// remember stores a fresh answer for host.
func (e *Excluder) remember(host string, addrs []string) {
	if len(addrs) == 0 {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cache == nil {
		e.cache = make(map[string]cacheEntry)
	}
	e.cache[host] = cacheEntry{Addrs: addrs, Seen: time.Now()}
}

// missed records that host answered with nothing just now and returns the
// address it last answered with, if that is still worth writing, along with
// whether that answer is old enough to be worth mentioning.
func (e *Excluder) missed(host string) (addrs []string, stale bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cache == nil {
		e.cache = make(map[string]cacheEntry)
	}
	entry := e.cache[host]
	entry.Failed = time.Now()
	if len(entry.Addrs) > 0 && time.Since(entry.Seen) >= cacheTTL {
		entry.Addrs, entry.Seen = nil, time.Time{}
	}
	e.cache[host] = entry
	return entry.Addrs, time.Since(entry.Seen) >= staleAfter
}

// load reads the remembered addresses once per process. A missing or unreadable
// file is an empty memory, which is what a fresh install has.
func (e *Excluder) load(dir string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.loaded {
		return
	}
	e.loaded = true
	if e.cache == nil {
		e.cache = make(map[string]cacheEntry)
	}
	data, err := os.ReadFile(filepath.Join(dir, "lists", nodeCacheFile))
	if err != nil {
		return
	}
	var f cacheFile
	if err := json.Unmarshal(data, &f); err != nil {
		return
	}
	for host, entry := range f.Hosts {
		if host == "" || (len(entry.Addrs) == 0 && entry.Failed.IsZero()) {
			continue
		}
		if len(entry.Addrs) > 0 && time.Since(entry.Seen) >= cacheTTL {
			continue
		}
		e.cache[host] = entry
	}
}

// save writes the remembered addresses back, pruned to what is worth keeping.
// Best-effort: this is a cache, and failing to persist it costs one slow connect
// after a restart, not a working bypass.
func (e *Excluder) save(dir string) {
	e.mu.Lock()
	hosts := make(map[string]cacheEntry, len(e.cache))
	for host, entry := range e.cache {
		if len(entry.Addrs) > 0 && time.Since(entry.Seen) >= cacheTTL {
			continue
		}
		hosts[host] = entry
	}
	e.mu.Unlock()

	if len(hosts) > maxCacheEntries {
		keys := make([]string, 0, len(hosts))
		for host := range hosts {
			keys = append(keys, host)
		}
		// Newest first, by whichever of the two timestamps is later: a name that
		// keeps being asked for stays, one nobody has configured for months goes.
		sort.Slice(keys, func(a, b int) bool {
			ta, tb := lastTouched(hosts[keys[a]]), lastTouched(hosts[keys[b]])
			if !ta.Equal(tb) {
				return ta.After(tb)
			}
			return keys[a] < keys[b]
		})
		for _, host := range keys[maxCacheEntries:] {
			delete(hosts, host)
		}
	}

	data, err := json.Marshal(cacheFile{Version: 1, Hosts: hosts})
	if err != nil {
		return
	}
	path := filepath.Join(dir, "lists", nodeCacheFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0o644)
}

// lastTouched is the later of an entry's two timestamps.
func lastTouched(e cacheEntry) time.Time {
	if e.Failed.After(e.Seen) {
		return e.Failed
	}
	return e.Seen
}

// usableNodeIP rejects answers no exit node can sit behind. Resolvers that turn
// a dead name into 0.0.0.0 or the loopback are common enough that taking their
// word would put a meaningless line in the ipset and count the node as handled,
// leaving the real one exposed with the warning suppressed.
func usableNodeIP(ip net.IP) bool {
	return ip != nil &&
		!ip.IsUnspecified() &&
		!ip.IsLoopback() &&
		!ip.IsLinkLocalUnicast() &&
		!ip.IsLinkLocalMulticast() &&
		!ip.IsMulticast()
}

// sortedKeys returns a set's members in a stable order.
func sortedKeys(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// dedupe drops repeats from a sorted slice.
func dedupe(sorted []string) []string {
	out := sorted[:0]
	var prev string
	for i, s := range sorted {
		if i > 0 && s == prev {
			continue
		}
		prev = s
		out = append(out, s)
	}
	return out
}

// userLines returns the file's content above our managed header — the entries
// the user added themselves. A missing file yields nothing, which is the same
// as an empty user section.
func userLines(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == excludeHeader {
			break
		}
		out = append(out, line)
	}
	// Trailing blank lines would accumulate one per rewrite.
	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}
	return out
}
