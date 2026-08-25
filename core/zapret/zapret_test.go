package zapret

import (
	"net/url"
	"regexp"
	"testing"
)

// bundleListing is what an unpacked zapret-discord-youtube release looks like.
func bundleListing() []string {
	return []string{
		"general.bat",
		"general (ALT).bat",
		"general (ALT2).bat",
		"general (ALT10).bat",
		"general (FAKE TLS AUTO).bat",
		"service.bat",
		"lists",
		"bin",
		"README.md",
	}
}

func TestDiscoverSkipsTheServiceScript(t *testing.T) {
	got := Discover(`C:\zapret`, bundleListing())

	for _, s := range got {
		if s.Name == "service" {
			// service.bat installs the whole bundle as a Windows service. Running
			// it as if it were a strategy would silently reconfigure the machine
			// instead of testing anything.
			t.Fatal("service.bat was offered as a strategy")
		}
	}
	if len(got) != 5 {
		t.Fatalf("found %d strategies, want 5: %+v", len(got), got)
	}
}

func TestDiscoverPutsTheDefaultFirst(t *testing.T) {
	got := Discover(`C:\zapret`, bundleListing())
	if got[0].Name != "general" {
		// Plain "general" is the bundle's default and the likeliest to work, so
		// an interrupted probe run has still tried the best candidate.
		t.Fatalf("first strategy is %q, want general", got[0].Name)
	}
}

func TestDiscoverIgnoresNonBatEntries(t *testing.T) {
	got := Discover(`C:\zapret`, []string{"lists", "bin", "README.md", "winws.exe"})
	if len(got) != 0 {
		t.Fatalf("got %+v, want nothing", got)
	}
}

// ok/bad build results with a given coverage and latency.
func res(name string, okCount int, rtt int64) Result {
	r := Result{Name: name, Started: true}
	for i := 0; i < 5; i++ {
		r.Targets = append(r.Targets, TargetResult{
			Target: "t",
			OK:     i < okCount,
			RTTMs:  rtt,
		})
	}
	return r
}

func TestRankPrefersCoverageOverSpeed(t *testing.T) {
	// The strategies differ in WHAT they unblock, not how fast: one may carry
	// YouTube but not Discord voice. Ranking by latency first would
	// systematically pick the narrower bypass.
	narrowFast := res("narrow", 2, 30)
	broadSlow := res("broad", 4, 300)

	got := Rank([]Result{narrowFast, broadSlow})
	if got[0].Name != "broad" {
		t.Fatalf("ranked %q first, want the strategy that unblocks more", got[0].Name)
	}
}

func TestRankBreaksCoverageTiesByLatency(t *testing.T) {
	got := Rank([]Result{res("slow", 3, 400), res("fast", 3, 40)})
	if got[0].Name != "fast" {
		t.Fatalf("ranked %q first, want the faster of two equal strategies", got[0].Name)
	}
}

func TestBestRefusesWhenNothingBeatsTheBaseline(t *testing.T) {
	// Everything already worked without zapret: enabling a packet-mangling
	// driver buys nothing and costs a kernel filter on every connection.
	results := []Result{res("a", 5, 50), res("b", 5, 60)}
	if _, ok := Best(results, 5); ok {
		t.Fatal("picked a strategy when the connection already worked")
	}

	// And when nothing helps at all, saying so beats handing back the least-bad
	// option and letting the user believe the block is handled.
	weak := []Result{res("a", 1, 50), res("b", 0, 0)}
	if _, ok := Best(weak, 1); ok {
		t.Fatal("picked a strategy that did not beat the baseline")
	}
}

func TestBestPicksTheImprovement(t *testing.T) {
	results := []Result{res("noop", 1, 20), res("works", 5, 200)}
	best, ok := Best(results, 1)
	if !ok || best.Name != "works" {
		t.Fatalf("Best = %q (ok=%v), want works", best.Name, ok)
	}
}

func TestScoreIgnoresFailedTargets(t *testing.T) {
	r := Result{Name: "x", Targets: []TargetResult{
		{OK: true, RTTMs: 100},
		{OK: false, RTTMs: 0},
		{OK: true, RTTMs: 900},
	}}
	if got := r.Score(); got != 100 {
		t.Fatalf("Score = %d, want the median of successes (100)", got)
	}
	dead := Result{Name: "y", Targets: []TargetResult{{OK: false}}}
	if got := dead.Score(); got != Unreachable {
		t.Fatalf("Score = %d, want Unreachable", got)
	}
}

func TestDefaultTargetsAreTheBlockedOnes(t *testing.T) {
	// Probing something universally reachable would score every strategy as
	// perfect, including those that change nothing.
	targets := DefaultTargets()
	if len(targets) < 3 {
		t.Fatalf("only %d targets", len(targets))
	}
	var discord, youtube bool
	for _, u := range targets {
		if contains(u, "discord") {
			discord = true
		}
		if contains(u, "youtube") || contains(u, "ytimg") || contains(u, "googlevideo") {
			youtube = true
		}
	}
	if !discord || !youtube {
		t.Errorf("targets miss the services this exists for: discord=%v youtube=%v", discord, youtube)
	}
}

func contains(h, n string) bool {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return true
		}
	}
	return false
}

// TestDefaultTargetsAreDurableNames guards the probe set against a name that
// resolves today and is gone next quarter.
//
// The video slot shipped as rr1---sn-4g5e6nls.googlevideo.com — one CDN node's
// hostname, of the kind a player is handed for a single session. Those are
// allocated per region and retired, and that one had already stopped resolving:
// the target failed for every strategy and for the baseline alike, so nothing
// looked wrong in the ranking while the one slot that stands for video actually
// streaming measured nothing at all. A strategy that fixes the YouTube page and
// leaves the video spinning would have won, which is precisely the complaint the
// picker exists to settle.
//
// This asserts on shape, not on DNS: a test that resolved names would fail on
// every machine without a network and pass on a machine whose resolver lies.
func TestDefaultTargetsAreDurableNames(t *testing.T) {
	// rrN---sn-XXXXXXX.googlevideo.com and friends: a per-session edge node.
	ephemeral := regexp.MustCompile(`(?i)://[a-z0-9]+-{2,}sn-`)
	for _, target := range DefaultTargets() {
		if ephemeral.MatchString(target) {
			t.Errorf("%s names an individual CDN node; those are retired without notice "+
				"and the target then fails for every strategy at once", target)
		}
		u, err := url.Parse(target)
		if err != nil {
			t.Errorf("%s: %v", target, err)
			continue
		}
		if u.Scheme != "https" {
			t.Errorf("%s: the censor acts on the TLS handshake, so the probe has to make one", target)
		}
		if u.Hostname() == "" {
			t.Errorf("%s: no host", target)
		}
	}
}
