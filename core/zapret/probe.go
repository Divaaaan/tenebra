package zapret

import (
	"context"
	"net"
	"net/http"
	"sync"
	"time"
)

// DialFunc opens the connections a probe is measured on. The signature matches
// net.Dialer.DialContext and http.Transport.DialContext, so a caller hands over
// a dialer it already owns instead of describing one.
type DialFunc func(ctx context.Context, network, address string) (net.Conn, error)

// defaultRequestTimeout bounds a set of control requests when the Runner carries
// no budget of its own. Zero is not "no limit" here: it reaches
// context.WithTimeout, which cancels the requests before they leave and scores
// every strategy in the bundle as broken.
const defaultRequestTimeout = 8 * time.Second

// requestTimeout is the budget one Probe call may spend, defaulted so a
// hand-built Runner — or one from the non-Windows constructor, which sets no
// timings — measures something instead of cancelling everything.
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
// direct path, never the tunnel — is not Windows-specific either. Behind a
// //go:build windows tag its tests would still run in CI, which has a Windows
// job, but only there: not in the Linux and macOS jobs, and not for anyone
// developing on those. Shared, every `go test ./...` checks it.
//
// Two things keep the measurement off the tunnel, and both are needed:
//
//   - No proxy on the transport. A probe through the proxy completes whatever
//     the strategy does and scores every one of them perfect.
//   - Dial, when set, pins the socket to the interface the packet filter is
//     confined to (Runner.PinIfaceIndex; core/control resolves the two from one
//     lookup so they cannot differ). Proxy: nil only refuses an HTTP proxy; it
//     says nothing about a tun that owns the default route, so with a tunnel up
//     every request — the baseline first of all — leaves through it. Every
//     target then answers, the baseline reads full marks, each strategy reads
//     full marks, and Best (which reports a strategy only when it BEATS the
//     baseline) concludes that nothing helps. That is the self-repair path,
//     repickStrategy, giving up on a measurement of the very tunnel it was
//     trying to get traffic off. A probe that leaves by an interface the filter
//     is not on lands in the same place by a shorter road: the strategy cannot
//     change what it never touched.
//
// A nil Dial leaves the transport on its own dialer, which is what this always
// did: on a machine with no physical interface to bind to, an ordinary routed
// dial is a real answer and a refused one is not.
//
// The targets are asked all at once under one shared budget, not one after
// another, and that is where the strategy pick's running time went. A blocked
// destination does not answer "no" — it hangs until the budget expires, so
// sequentially the cost of a probe is the number of blocked targets times the
// timeout. With five control targets at eight seconds each that is forty seconds
// per strategy, and a run measures every strategy in the bundle: the same
// measurement that took six minutes takes one budget per strategy instead of
// five. Nothing about the verdict changes — the targets are independent
// destinations and none of them is affected by another being asked at the same
// moment — only the waiting is shared. Same reasoning, and the same shape, as
// control.bypassCarriesEvery.
//
// Results are written into a pre-sized slice by index rather than appended from
// the goroutines, so the answer keeps the order the caller asked in. Callers read
// it positionally — the UI lists the measurements against its own target list,
// the diagnostics bundle prints the pair — and completion order would file the
// fast destination's round-trip under the slow one's name without anything
// failing.
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

	// One deadline for the whole set. It is the budget a single target used to
	// get, because the set is measured in parallel: the slowest target decides
	// how long the probe takes, not the sum.
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	out := make([]TargetResult, len(targets))
	var wg sync.WaitGroup
	for i, t := range targets {
		// Seeded before the goroutine starts, so a target that cannot even be
		// turned into a request still reports itself by name.
		out[i] = TargetResult{Target: t}
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, t, nil)
			if err != nil {
				return
			}
			start := time.Now()
			resp, err := client.Do(req)
			rtt := time.Since(start).Milliseconds()

			// Any HTTP status counts as reachable: a censored destination fails by
			// timing out or being reset, not by answering 403.
			if err != nil {
				return
			}
			_ = resp.Body.Close()
			out[i] = TargetResult{Target: t, OK: true, RTTMs: rtt}
		}()
	}
	wg.Wait()
	return out
}
