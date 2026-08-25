// Package zapret drives a bundled zapret installation: the set of DPI-bypass
// strategies shipped as .bat files around winws.exe.
//
// Why this belongs in a VPN client at all: a tunnel and a DPI bypass solve the
// same problem in opposite ways. The tunnel moves traffic to another country --
// correct, but it adds the whole round trip to every packet, which is what makes
// voice chat and video unpleasant. zapret instead confuses the censor's
// inspection so the traffic reaches its real destination directly, at the ISP's
// own latency. For the services it can handle, that is strictly better, and the
// tunnel is left for what it cannot.
//
// The hard part is that there is no single strategy: the bundle ships around
// twenty because each provider's DPI implementation falls for something
// different, and which one works is discoverable only by trying. Doing that by
// hand means running a batch file, opening a site, guessing, repeating. This
// package makes it a measurement.
package zapret

import (
	"path/filepath"
	"sort"
	"strings"
)

// Strategy is one bypass configuration from the bundle.
type Strategy struct {
	// Name is the display name, taken from the file name without extension.
	Name string
	// Path is the absolute path of the .bat that launches it.
	Path string
}

// serviceScripts are bundle files that are not strategies. service.bat installs
// the whole thing as a Windows service and would, if launched as a strategy,
// silently change the system's configuration instead of testing anything.
var serviceScripts = map[string]bool{
	"service":       true,
	"check_updates": true,
	"install_bin":   true,
	"cleanup":       true,
	"preset_russia": true,
	"blockcheck":    true,
	"diagnostics":   true,
}

// Discover turns a directory listing into the strategies worth probing.
//
// It takes the file names rather than reading the disk so the selection rules
// stay testable without a bundle on hand — the platform layer supplies the
// listing.
//
// Ordering is alphabetical with the plain "general" first: it is the bundle's
// default and the one most likely to work, so a probe run that is interrupted
// early has still tried the best candidate.
func Discover(dir string, names []string) []Strategy {
	var out []Strategy
	for _, n := range names {
		if !strings.EqualFold(filepath.Ext(n), ".bat") {
			continue
		}
		base := strings.TrimSuffix(filepath.Base(n), filepath.Ext(n))
		if serviceScripts[strings.ToLower(base)] {
			continue
		}
		// Files this app writes into the bundle start with a dot (see
		// derivedStrategyFile). They are rewrites of a strategy already in the list,
		// so listing them would offer the user the same bypass twice and make a probe
		// run measure one candidate under two names.
		if strings.HasPrefix(base, ".") {
			continue
		}
		out = append(out, Strategy{Name: base, Path: filepath.Join(dir, n)})
	}

	sort.SliceStable(out, func(a, b int) bool {
		la, lb := strings.ToLower(out[a].Name), strings.ToLower(out[b].Name)
		if la == "general" != (lb == "general") {
			return la == "general"
		}
		return la < lb
	})
	return out
}

// TargetResult is one control request made while a strategy was active.
type TargetResult struct {
	// Target is the probed URL.
	Target string `json:"target"`
	// OK is true when the request completed — any HTTP status counts, because a
	// censored destination fails by timing out or resetting, not by answering.
	OK bool `json:"ok"`
	// RTTMs is the round-trip in milliseconds; meaningful only when OK.
	RTTMs int64 `json:"rtt_ms"`
}

// Result is a strategy's measured outcome.
type Result struct {
	Strategy Strategy `json:"-"`
	Name     string   `json:"strategy"`
	// Started reports whether winws actually came up. A strategy whose process
	// never started scores zero for a reason worth telling apart from "started
	// and did not help".
	Started bool           `json:"started"`
	Targets []TargetResult `json:"targets"`
}

// OKCount is how many targets completed.
func (r Result) OKCount() int {
	n := 0
	for _, t := range r.Targets {
		if t.OK {
			n++
		}
	}
	return n
}

// Score is the median round-trip over the targets that completed, or
// Unreachable when none did.
//
// Median rather than mean for the same reason node selection uses it: one slow
// destination should not decide between two otherwise equal strategies.
func (r Result) Score() int64 {
	rtts := make([]int64, 0, len(r.Targets))
	for _, t := range r.Targets {
		if t.OK && t.RTTMs > 0 {
			rtts = append(rtts, t.RTTMs)
		}
	}
	if len(rtts) == 0 {
		return Unreachable
	}
	sort.Slice(rtts, func(a, b int) bool { return rtts[a] < rtts[b] })
	return rtts[(len(rtts)-1)/2]
}

// Unreachable sorts after every real measurement.
const Unreachable int64 = 1<<62 - 1

// Rank orders strategies best-first: coverage, then latency, then bundle order.
//
// Coverage dominates because the strategies differ in *what* they unblock, not
// in speed: one may carry YouTube but not Discord voice. A strategy that carries
// four of five destinations slowly is worth more than one that carries two
// quickly, and ranking by latency first would systematically pick the narrower
// bypass.
func Rank(results []Result) []Result {
	out := make([]Result, len(results))
	copy(out, results)

	idx := make(map[string]int, len(results))
	for i, r := range results {
		if _, seen := idx[r.Name]; !seen {
			idx[r.Name] = i
		}
	}

	sort.SliceStable(out, func(a, b int) bool {
		ra, rb := out[a], out[b]
		if ca, cb := ra.OKCount(), rb.OKCount(); ca != cb {
			return ca > cb
		}
		if sa, sb := ra.Score(), rb.Score(); sa != sb {
			return sa < sb
		}
		return idx[ra.Name] < idx[rb.Name]
	})
	return out
}

// Best returns the strategy to keep, and whether it is worth keeping at all.
//
// baseline is how many targets already worked with zapret off. A strategy is
// only reported when it beats that: if everything already works, enabling a
// packet-mangling driver buys nothing and costs a kernel filter on every
// connection. And if nothing beats the baseline, saying so is far more useful
// than handing back the least-bad option and letting the user believe the block
// is now handled.
func Best(results []Result, baseline int) (Result, bool) {
	ranked := Rank(results)
	if len(ranked) == 0 {
		return Result{}, false
	}
	top := ranked[0]
	if top.OKCount() <= baseline {
		return Result{}, false
	}
	return top, true
}

// ShouldStopEarly reports whether a probe run can end on this result instead of
// measuring the rest of the bundle.
//
// A run is a search for the strategy that covers the most, and Rank settles that
// on coverage before anything else. A strategy that carries every target has
// therefore already won: no strategy later in the bundle can score above it, and
// each one that is measured anyway costs the settle time plus a probe budget —
// most of a six-minute run, spent confirming an answer already in hand. What it
// gives up is Rank's tie-break: a full-coverage strategy further down the bundle
// might have been a few milliseconds quicker. That is a trade of round-trip
// against minutes of a progress bar, and the user is sitting in front of the
// second one.
//
// targets is how many destinations the run scores a strategy on; baseline how
// many of them already worked with the bypass off.
//
// The baseline is the edge that matters. When it already equals the number of
// targets, nothing on this network is blocked: every strategy scores full marks,
// and so does running no bypass at all. The first strategy measured would then
// look perfect, the run would stop, and alphabetical order would have decided
// which kernel packet filter the user runs for no benefit. Best refuses a
// strategy that merely ties the baseline — it has to BEAT it — so a run that
// stopped there would have thrown away its own answer along with every strategy
// it never got to. Above a baseline below the target count, beating it is
// automatic: full coverage is more than a baseline that is not full.
func ShouldStopEarly(r Result, targets, baseline int) bool {
	if targets <= 0 {
		return false // nothing to cover, so nothing to have covered
	}
	if baseline >= targets {
		return false // nothing is blocked here; no strategy can beat doing nothing
	}
	return r.OKCount() >= targets
}

// DefaultTargets are the destinations a strategy is judged on.
//
// They are deliberately the ones that break: Discord's API and CDN, YouTube's
// page, its video hosts and its image CDN. Probing something universally
// reachable would score every strategy as perfect, including the ones that
// change nothing.
//
// Probes must be made WITHOUT any proxy. Through a tunnel they would all
// succeed regardless of the strategy — measuring the tunnel, not the bypass.
//
// Every name here has to be one that resolves anywhere, which sounds obvious and
// was not: the video-host slot held rr1---sn-4g5e6nls.googlevideo.com, an
// individual CDN node's hostname of the kind a player is handed for one session.
// Those names are allocated per region and retired, and that one had stopped
// existing — it answered NXDOMAIN, so the target failed for every strategy and
// for the no-bypass baseline alike. The scoring survived (everyone lost the same
// point), but the measurement did not: this is the slot that stands for video
// actually streaming, and streaming is what a censor throttles when the page
// still loads. A run could hand back a strategy that fixes the YouTube page and
// leaves the video spinning, which is the exact complaint the picker exists to
// answer. redirector.googlevideo.com is Google's stable entry point into the
// same SNI space, so it can be reached from anywhere and is still judged by the
// name a censor acts on.
func DefaultTargets() []string {
	return []string{
		"https://discord.com/api/v9/gateway",
		"https://cdn.discordapp.com/robots.txt",
		"https://" + VideoPageHost + "/generate_204",
		"https://i.ytimg.com/generate_204",
		"https://" + VideoStreamHost + "/generate_204",
	}
}

// VideoPageHost and VideoStreamHost are the two halves of "does YouTube work":
// the page a user opens, and the host the video itself comes down from.
//
// They are named and exported because the pick is not the only thing that has to
// ask for them. The daemon checks the bypass again after every connect, and that
// check has to measure the same pair this run is scored on: two independently
// spelled ideas of what counts as video drift apart at the first edit, and the
// half that drifts is the half nobody notices — a check still asking only for
// the page keeps calling a bypass healthy while the picker has moved on.
//
// Keeping the pair apart is the point. A censor that throttles the stream and
// leaves the page loading is not an exotic case; it is the ordinary one, and it
// is what people mean when they say YouTube does not work.
const (
	VideoPageHost   = "www.youtube.com"
	VideoStreamHost = "redirector.googlevideo.com"
)
