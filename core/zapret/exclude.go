package zapret

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// excludeFile is the bundle's user-editable exclusion list. Every shipped
// strategy passes it to winws as --ipset-exclude, so an address listed here is
// left alone by the packet filter.
const excludeFile = "ipset-exclude-user.txt"

// ExcludeHeader marks the block this package owns inside that file, so a user's
// own entries above it survive being rewritten.
const excludeHeader = "# --- tenebra: VPN node addresses, managed automatically ---"

// ExcludeNodes writes the exit-node addresses into the bundle's exclusion list,
// preserving any lines the user put there themselves.
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
// Only IP literals are written. Resolving a hostname here would mean a DNS
// lookup on a path that may not be up yet, and a wrong or stale answer would
// exclude someone else's address while leaving the node exposed.
func ExcludeNodes(dir string, servers []string) error {
	path := filepath.Join(dir, "lists", excludeFile)

	kept := userLines(path)

	seen := make(map[string]struct{})
	var ips []string
	for _, s := range servers {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		// Bracketed IPv6 ("[2001:db8::1]") arrives from URL-shaped sources.
		s = strings.TrimSuffix(strings.TrimPrefix(s, "["), "]")
		if net.ParseIP(s) == nil {
			continue // a hostname: see above
		}
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		ips = append(ips, s)
	}
	sort.Strings(ips)

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
	// header keeps the file non-empty even with no IP-literal nodes.

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("zapret: не создать каталог списков: %w", err)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("zapret: не записать %s: %w", excludeFile, err)
	}
	return nil
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
