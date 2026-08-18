//go:build windows

package zapret

import (
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"path/filepath"
	"time"
)

// Runner starts and stops zapret strategies and measures what they unblock.
//
// Strategies are launched through their .bat rather than by calling winws.exe
// with parsed arguments. The batch does more than build a command line: it loads
// the bundle's host lists, applies the user's own additions and resolves the
// game-filter variables. Re-implementing that here would drift from upstream the
// first time the bundle changes, and the drift would be silent — a strategy that
// still starts but no longer covers what it should.
type Runner struct {
	// Dir is the installed bundle directory.
	Dir string
	// Settle is how long to wait after launching before probing. The driver
	// attaches asynchronously, so probing immediately scores a good strategy as
	// broken.
	Settle time.Duration
	// ProbeTimeout bounds one control request.
	ProbeTimeout time.Duration
}

// NewRunner builds a Runner with defaults that hold up on a slow machine.
func NewRunner(dir string) *Runner {
	return &Runner{Dir: dir, Settle: 7 * time.Second, ProbeTimeout: 8 * time.Second}
}

// Start launches a strategy and reports whether winws came up.
func (r *Runner) Start(ctx context.Context, s Strategy) (bool, error) {
	if err := r.Stop(ctx); err != nil {
		return false, err
	}
	cmd := exec.CommandContext(ctx, "cmd.exe", "/c", s.Path)
	cmd.Dir = r.Dir
	if err := cmd.Start(); err != nil {
		return false, fmt.Errorf("zapret: не запустить %s: %w", s.Name, err)
	}
	// The batch spawns winws and returns; waiting on it would hang.
	go func() { _ = cmd.Wait() }()

	return r.waitForWinws(ctx, r.Settle), nil
}

// Stop terminates winws and waits for the packet filter to actually detach.
//
// The wait matters: WinDivert unloads asynchronously, and starting the next
// strategy while the previous filter is still attached makes the new winws exit
// immediately. Probing then records "did not start" for a strategy that is
// perfectly good — which is exactly how a first probe run mislabelled more than
// half the bundle.
func (r *Runner) Stop(ctx context.Context) error {
	_ = exec.CommandContext(ctx, "taskkill", "/F", "/IM", "winws.exe").Run()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if !winwsRunning(ctx) {
			// Give the driver a beat to detach after the process is gone.
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(1500 * time.Millisecond):
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
	return nil
}

// waitForWinws polls until the process appears or the budget runs out.
func (r *Runner) waitForWinws(ctx context.Context, budget time.Duration) bool {
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if winwsRunning(ctx) {
			// Attached, but the filter needs a moment before it affects traffic.
			select {
			case <-ctx.Done():
				return false
			case <-time.After(1500 * time.Millisecond):
			}
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(400 * time.Millisecond):
		}
	}
	return false
}

func winwsRunning(ctx context.Context) bool {
	out, err := exec.CommandContext(ctx, "tasklist", "/FI", "IMAGENAME eq winws.exe", "/NH").Output()
	if err != nil {
		return false
	}
	return len(out) > 0 && containsFold(string(out), "winws.exe")
}

func containsFold(h, n string) bool {
	for i := 0; i+len(n) <= len(h); i++ {
		match := true
		for j := 0; j < len(n); j++ {
			a, b := h[i+j], n[j]
			if a >= 'A' && a <= 'Z' {
				a += 32
			}
			if b >= 'A' && b <= 'Z' {
				b += 32
			}
			if a != b {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// Probe measures the control targets on the current network path.
//
// The client is built with a transport that uses NO proxy: the entire question
// is whether traffic survives without a tunnel, and a probe through the proxy
// would succeed regardless of the strategy and score every one of them perfect.
func (r *Runner) Probe(ctx context.Context, targets []string) []TargetResult {
	client := &http.Client{
		Transport: &http.Transport{
			Proxy:               nil,
			DisableKeepAlives:   true,
			TLSHandshakeTimeout: r.ProbeTimeout,
		},
		Timeout: r.ProbeTimeout,
	}

	out := make([]TargetResult, 0, len(targets))
	for _, t := range targets {
		reqCtx, cancel := context.WithTimeout(ctx, r.ProbeTimeout)
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

// Pick probes every strategy and returns the results plus the baseline coverage
// measured with zapret off.
//
// The baseline is taken first and is not decoration: without it there is no way
// to tell "this strategy fixed the block" from "this connection was never
// blocked", and enabling a kernel packet filter that changes nothing is a cost
// with no benefit.
//
// progress, when non-nil, is called after each strategy so a UI can show the run
// advancing — a silent five-minute operation reads as a hang.
func (r *Runner) Pick(ctx context.Context, strategies []Strategy, targets []string, progress func(Result)) (results []Result, baseline int, err error) {
	if err := r.Stop(ctx); err != nil {
		return nil, 0, err
	}
	base := r.Probe(ctx, targets)
	for _, t := range base {
		if t.OK {
			baseline++
		}
	}

	for _, s := range strategies {
		if ctx.Err() != nil {
			return results, baseline, ctx.Err()
		}
		started, startErr := r.Start(ctx, s)
		res := Result{Strategy: s, Name: s.Name, Started: started}
		if started && startErr == nil {
			res.Targets = r.Probe(ctx, targets)
		}
		results = append(results, res)
		if progress != nil {
			progress(res)
		}
	}

	// Leave the machine as it was found: the caller decides what to run, and a
	// probe run that silently left the last (probably worst) strategy attached
	// would be a surprising side effect.
	_ = r.Stop(ctx)
	return results, baseline, nil
}

// StrategyPath resolves a strategy name against the bundle directory.
func (r *Runner) StrategyPath(name string) string {
	return filepath.Join(r.Dir, name+".bat")
}
