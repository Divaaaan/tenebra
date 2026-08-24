package dnswire

import (
	"context"
	"net"
	"os"
	"testing"
	"time"
)

// TestLiveResolvers asks the real resolvers this client ships with, over each
// transport, and prints what they answered and how long it took. It is skipped
// unless TENEBRA_LIVE is set, so the ordinary run stays offline.
//
//	TENEBRA_LIVE=1 go test ./core/dnswire -run TestLiveResolvers -v
//
// It exists because the parts that break here break against real servers, not
// against a fixture: a resolver whose certificate does not cover the IP its
// address names, an endpoint that answers only over HTTP/3, a network where the
// encrypted resolver is filtered and the timing is the whole story.
func TestLiveResolvers(t *testing.T) {
	if os.Getenv("TENEBRA_LIVE") == "" {
		t.Skip("set TENEBRA_LIVE=1 to run the live resolver check")
	}
	const host = "example.com"

	for _, addr := range []string{
		"https://77.88.8.8/dns-query",           // the shipped direct default
		"https://dns.adguard-dns.com/dns-query", // named endpoint, bootstrapped by name
		"tls://dns.quad9.net",                   // DoT
		"77.88.8.8",                             // plain, bare address form
	} {
		r, ok := NewResolver(addr)
		if !ok {
			t.Errorf("%s: NewResolver refused it", addr)
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		start := time.Now()
		ips, err := r.LookupIP(ctx, host)
		elapsed := time.Since(start)
		cancel()
		if err != nil {
			t.Logf("%-40s FAILED after %v: %v", addr, elapsed.Round(time.Millisecond), err)
			continue
		}
		t.Logf("%-40s %v in %v", addr, ips, elapsed.Round(time.Millisecond))
	}

	start := time.Now()
	ips, err := net.DefaultResolver.LookupIP(context.Background(), "ip", host)
	t.Logf("%-40s %v in %v (err %v)", "system resolver", ips, time.Since(start).Round(time.Millisecond), err)
}
