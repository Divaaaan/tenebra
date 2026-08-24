package zapret

import (
	"context"
	"net"
	"net/http"
	"time"
)

// DialFunc opens the connections a probe is measured on. The signature matches
// net.Dialer.DialContext and http.Transport.DialContext, so a caller hands over
// a dialer it already owns instead of describing one.
type DialFunc func(ctx context.Context, network, address string) (net.Conn, error)

// defaultRequestTimeout bounds one control request when the Runner carries no
// budget of its own. Zero is not "no limit" here: it reaches
// context.WithTimeout, which cancels the request before it leaves and scores
// every strategy in the bundle as broken.
const defaultRequestTimeout = 8 * time.Second

// requestTimeout is the per-target budget, defaulted so a hand-built Runner —
// or one from the non-Windows constructor, which sets no timings — measures
// something instead of cancelling everything.
func (r *Runner) requestTimeout() time.Duration {
	if r.ProbeTimeout > 0 {
		return r.ProbeTimeout
	}
	return defaultRequestTimeout
}

// Probe measures the control targets on the path the bypass actually acts on.
//
// It lives outside the platform files because it is plain HTTP with nothing
// Windows-specific in it, and because the one rule it has to keep — measure the
// direct path, never the tunnel — is worth checking wherever the test suite
// runs rather than only where someone runs the Windows-tagged tests.
//
// Two things keep the measurement off the tunnel, and both are needed:
//
//   - No proxy on the transport. A probe through the proxy completes whatever
//     the strategy does and scores every one of them perfect.
//   - Dial, when set, pins the socket to the physical uplink. Proxy: nil only
//     refuses an HTTP proxy; it says nothing about a tun that owns the default
//     route, so with a tunnel up every request — the baseline first of all —
//     leaves through it. Every target then answers, the baseline reads full
//     marks, each strategy reads full marks, and Best (which reports a strategy
//     only when it BEATS the baseline) concludes that nothing helps. That is the
//     self-repair path, repickStrategy, giving up on a measurement of the very
//     tunnel it was trying to get traffic off.
//
// A nil Dial leaves the transport on its own dialer, which is what this always
// did: on a machine with no physical interface to bind to, an ordinary routed
// dial is a real answer and a refused one is not.
func (r *Runner) Probe(ctx context.Context, targets []string) []TargetResult {
	timeout := r.requestTimeout()
	transport := &http.Transport{
		Proxy:               nil,
		DisableKeepAlives:   true,
		TLSHandshakeTimeout: timeout,
	}
	if r.Dial != nil {
		transport.DialContext = r.Dial
	}
	client := &http.Client{Transport: transport, Timeout: timeout}

	out := make([]TargetResult, 0, len(targets))
	for _, t := range targets {
		reqCtx, cancel := context.WithTimeout(ctx, timeout)
		req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, t, nil)
		if err != nil {
			cancel()
			out = append(out, TargetResult{Target: t})
			continue
		}
		start := time.Now()
		resp, err := client.Do(req)
		rtt := time.Since(start).Milliseconds()
		cancel()

		// Any HTTP status counts as reachable: a censored destination fails by
		// timing out or being reset, not by answering 403.
		if err != nil {
			out = append(out, TargetResult{Target: t})
			continue
		}
		_ = resp.Body.Close()
		out = append(out, TargetResult{Target: t, OK: true, RTTMs: rtt})
	}
	return out
}
