//go:build go1.25

package subscription

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestDeadlineIncludesFallback(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		var dials atomic.Int32
		network := syntheticNetwork{
			lookup: func(ctx context.Context, host string) ([]net.IP, error) {
				time.Sleep(25 * time.Second)
				return nil, &net.DNSError{Err: "synthetic resolver failure", Name: host}
			},
			dial: func(ctx context.Context, kind, address string) (net.Conn, error) {
				dials.Add(1)
				<-ctx.Done()
				return nil, ctx.Err()
			},
		}
		_, _, err := fetchWithConfig(context.Background(), "https://example.test/private", fetchConfig{network: network, dohEndpoints: DefaultDoHEndpoints})
		if err == nil || dials.Load() == 0 {
			t.Fatal("fixture did not reach fallback")
		}
		if elapsed := time.Since(start); elapsed != 30*time.Second {
			t.Fatalf("primary/fallback reset the shared deadline: fake elapsed=%s", elapsed)
		}
	})
}
