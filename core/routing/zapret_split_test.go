package routing

import "testing"

// rulesTargeting collects every rule whose outbound is tag, in order.
func rulesTargeting(rules []map[string]any, tag string) []map[string]any {
	var out []map[string]any
	for _, r := range rules {
		if r["outbound"] == tag {
			out = append(out, r)
		}
	}
	return out
}

// suffixesTo gathers every domain_suffix pinned to the given outbound.
func suffixesTo(rules []map[string]any, tag string) []string {
	var out []string
	for _, r := range rulesTargeting(rules, tag) {
		out = append(out, suffixesOf(r)...)
	}
	return out
}

// bundleCoverage is what zapret.Covered reads out of a real 1.10.x bundle:
// YouTube/Google and Discord, and nothing else. The lists ship no entry for
// Instagram, X, Anthropic or OpenAI.
var bundleCoverage = []string{
	"youtube.com", "youtu.be", "googlevideo.com", "ytimg.com", "yt3.ggpht.com",
	"youtubei.googleapis.com",
	"discord.com", "discord.gg", "discord.media", "discordapp.com",
	"discordapp.net", "discordcdn.com", "discordstatus.com",
}

// The bypass carries what its host lists carry — and the tunnel must keep
// carrying the rest. Routing everything the preset knows about direct just
// because a bypass is running is how Claude, ChatGPT and Instagram break the
// moment the bypass comes up: nothing in the bundle touches them, so they meet
// the censor unassisted.
func TestZapretOnlyTakesWhatTheBundleCovers(t *testing.T) {
	o := Options{
		Mode:            ModeSmart,
		UnblockServices: true,
		ZapretActive:    true,
		ZapretCovered:   bundleCoverage,
	}.Normalize()
	rules := o.RouteRules()

	direct := suffixesTo(rules, tagDirect)
	proxy := suffixesTo(rules, tagProxy)

	for _, want := range []string{"youtube.com", "googlevideo.com", "discord.com", "discord.media"} {
		if !contains(direct, want) {
			t.Errorf("%q is covered by the bundle but is not routed direct", want)
		}
		if contains(proxy, want) {
			t.Errorf("%q is both bypassed and tunnelled", want)
		}
	}
	for _, want := range []string{"anthropic.com", "openai.com", "instagram.com", "x.com"} {
		if !contains(proxy, want) {
			t.Errorf("%q is not covered by the bundle and must stay in the tunnel", want)
		}
		if contains(direct, want) {
			t.Errorf("%q was handed to a bypass that does not cover it", want)
		}
	}
}

// A subdomain of a covered domain is covered: the lists name youtube.com, and
// music.youtube.com is the same service behind the same bypass rule.
func TestZapretCoverageMatchesSubdomains(t *testing.T) {
	o := Options{
		Mode:            ModeSmart,
		UnblockServices: true,
		ZapretActive:    true,
		ZapretCovered:   []string{"youtube.com"},
	}.Normalize()

	direct := suffixesTo(o.RouteRules(), tagDirect)
	if !contains(direct, "music.youtube.com") {
		t.Error("music.youtube.com is not treated as covered by youtube.com")
	}
	if contains(direct, "discord.com") {
		t.Error("discord.com was routed direct although this coverage does not include it")
	}
}

// Coverage that could not be read must not be taken as full coverage. Falling
// back to the narrow YouTube/Discord set keeps the certain part on the fast path
// and everything speculative in the tunnel.
func TestZapretUnknownCoverageFallsBackNarrow(t *testing.T) {
	o := Options{
		Mode:            ModeSmart,
		UnblockServices: true,
		ZapretActive:    true,
	}.Normalize()

	direct := suffixesTo(o.RouteRules(), tagDirect)
	proxy := suffixesTo(o.RouteRules(), tagProxy)

	if !contains(direct, "youtube.com") || !contains(direct, "discord.com") {
		t.Error("the fallback coverage should still put YouTube and Discord on the direct path")
	}
	if contains(direct, "anthropic.com") || contains(direct, "instagram.com") {
		t.Error("unknown coverage was treated as covering everything")
	}
	if !contains(proxy, "anthropic.com") {
		t.Error("anthropic.com left the tunnel without any bypass known to cover it")
	}
}

// A name resolved over the proxy describes the proxy's vantage point. For a
// service that will be reached directly, that is the wrong answer: googlevideo
// resolved from Frankfurt returns a Frankfurt cache, so the video would stream
// from another country over the direct path — the latency the bypass exists to
// remove.
func TestZapretServicesResolveDirect(t *testing.T) {
	o := Options{
		Mode:            ModeSmart,
		UnblockServices: true,
		ZapretActive:    true,
		ZapretCovered:   bundleCoverage,
	}.Normalize()

	var directNames, proxyNames []string
	for _, r := range o.dnsRules() {
		names, _ := r["domain_suffix"].([]string)
		switch r["server"] {
		case dnsDirectTag:
			directNames = append(directNames, names...)
		case dnsRemoteTag:
			proxyNames = append(proxyNames, names...)
		}
	}

	for _, want := range []string{"youtube.com", "googlevideo.com", "discord.com"} {
		if !contains(directNames, want) {
			t.Errorf("%q is routed direct but still resolves over the proxy", want)
		}
	}
	if !contains(proxyNames, "anthropic.com") {
		t.Error("a tunnelled service must keep resolving through the tunnel's resolver")
	}
}

// Turning the bypass off has to hand the services back to the tunnel. Leaving
// them direct points them at a censor with nothing carrying them through, which
// is worse than never having run the bypass: the VPN reports itself connected
// while YouTube and Discord are dead.
func TestZapretOffReturnsServicesToTunnel(t *testing.T) {
	o := Options{
		Mode:            ModeSmart,
		UnblockServices: true,
		ZapretActive:    false,
		ZapretCovered:   bundleCoverage, // stale coverage from an earlier run
	}.Normalize()

	rules := o.RouteRules()
	if contains(suffixesTo(rules, tagDirect), "youtube.com") {
		t.Error("youtube.com stayed on the direct path with the bypass off")
	}
	if !contains(suffixesTo(rules, tagProxy), "youtube.com") {
		t.Error("youtube.com was not returned to the tunnel")
	}
}
