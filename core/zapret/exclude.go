package zapret

import (
	"context"
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

// resolveBudget caps the wall clock the whole hostname lookup may spend. This
// runs on the connect path, right before the bypass comes up, so the budget is
// time added to every connect on a machine whose resolver is slow or gone; a
// cached or healthy answer arrives in tens of milliseconds and never approaches
// it. Two seconds buys a full round trip to an upstream resolver and a retry,
// and is short enough to read as part of connecting rather than a hang. A
// variable so tests can shrink it.
var resolveBudget = 2 * time.Second

// resolveParallel bounds the lookups in flight. A subscription can carry a
// hundred nodes: one after another, the first slow name eats the whole budget,
// while all hundred at once is a burst a local resolver answers with drops.
const resolveParallel = 16

// lookupIP resolves a node hostname to its addresses. A variable so tests can
// exercise the path without a DNS server.
var lookupIP = func(ctx context.Context, host string) ([]net.IP, error) {
	return net.DefaultResolver.LookupIP(ctx, "ip", host)
}

// ExcludeNodes writes the exit-node addresses into the bundle's exclusion list,
// preserving any lines the user put there themselves. It returns the node
// hostnames it could not resolve — the nodes still left in the filter's way — so
// the caller can name them instead of leaving the gap silent.
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
// The lookup happens at this moment rather than earlier because this is the last
// point where the direct path is known good: the bypass is not running yet and
// neither is the tunnel, so the answer comes over the same path sing-box will use
// to dial the node seconds later. It is bounded by resolveBudget, so a dead
// resolver costs a short pause and a warning rather than the connect.
func ExcludeNodes(dir string, servers []string) ([]string, error) {
	path := filepath.Join(dir, "lists", excludeFile)

	kept := userLines(path)
	ips, unresolved := nodeAddresses(servers)

	var b strings.Builder
	for _, l := range kept {
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
		return unresolved, fmt.Errorf("zapret: не создать каталог списков: %w", err)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return unresolved, fmt.Errorf("zapret: не записать %s: %w", excludeFile, err)
	}
	return unresolved, nil
}

// nodeAddresses turns the configured node endpoints into the address literals an
// ipset accepts: IP literals pass straight through, hostnames are resolved. The
// second return is the names that answered with nothing usable, deduplicated and
// sorted, for the caller to report.
func nodeAddresses(servers []string) (ips []string, unresolved []string) {
	seen := make(map[string]struct{})
	add := func(ip string) {
		if _, dup := seen[ip]; dup {
			return
		}
		seen[ip] = struct{}{}
		ips = append(ips, ip)
	}

	var hosts []string
	hostSeen := make(map[string]struct{})
	for _, s := range servers {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		// Bracketed IPv6 ("[2001:db8::1]") arrives from URL-shaped sources.
		s = strings.TrimSuffix(strings.TrimPrefix(s, "["), "]")
		if net.ParseIP(s) != nil {
			add(s)
			continue
		}
		// DNS is case-insensitive, so two spellings of one name are one lookup.
		host := strings.ToLower(s)
		if _, dup := hostSeen[host]; dup {
			continue
		}
		hostSeen[host] = struct{}{}
		hosts = append(hosts, host)
	}

	resolved := resolveHosts(hosts)
	for _, h := range hosts {
		addrs := resolved[h]
		if len(addrs) == 0 {
			unresolved = append(unresolved, h)
			continue
		}
		// Every address a name answers with is written: any of them is an endpoint
		// the tunnel may end up dialling, and excluding one the tunnel does not use
		// only means the bypass leaves that address alone.
		for _, a := range addrs {
			add(a)
		}
	}

	sort.Strings(ips)
	sort.Strings(unresolved)
	return ips, unresolved
}

// resolveHosts looks the names up in parallel under one shared deadline and
// returns the usable addresses each answered with. A name that failed, timed out
// or answered with nothing usable is simply absent from the map — the caller
// counts those rather than guessing an address for them.
//
// The deadline bounds the call itself, not only the lookups: the wait gives up
// when the budget runs out and reports what arrived, so a resolver that ignores
// its context (a swapped-in implementation, a platform stub) costs the budget
// rather than the connect. Stragglers finish into a map nobody reads.
func resolveHosts(hosts []string) map[string][]string {
	if len(hosts) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), resolveBudget)
	defer cancel()

	// Read once, up front: a straggler can outlive this call, and reaching for the
	// package variable from inside it would mean reading a value that may have been
	// replaced in the meantime. One batch, one resolver.
	lookup := lookupIP

	out := make(map[string][]string, len(hosts))
	var mu sync.Mutex
	var wg sync.WaitGroup
	slots := make(chan struct{}, resolveParallel)

	for _, h := range hosts {
		wg.Add(1)
		go func(host string) {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()

			// A lookup that starts after the deadline returns immediately with the
			// context's error, so a long queue drains at the budget instead of
			// running past it.
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
			out[host] = usable
			mu.Unlock()
		}(h)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}

	// Copied under the lock because a straggler may still be writing: what the
	// caller gets is a snapshot taken when the budget ran out.
	mu.Lock()
	defer mu.Unlock()
	snapshot := make(map[string][]string, len(out))
	for host, addrs := range out {
		snapshot[host] = addrs
	}
	return snapshot
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
