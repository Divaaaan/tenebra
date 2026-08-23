package routing

import (
	"strings"
	"testing"
)

// ruleFor returns the first route rule whose match carries the given key, or nil.
func ruleFor(rules []map[string]any, key string) map[string]any {
	for _, r := range rules {
		if _, ok := r[key]; ok {
			return r
		}
	}
	return nil
}

// suffixesOf extracts a rule's domain_suffix list.
func suffixesOf(r map[string]any) []string {
	if r == nil {
		return nil
	}
	out, _ := r["domain_suffix"].([]string)
	return out
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// indexOfRule returns the position of the first rule carrying key, or -1.
func indexOfRule(rules []map[string]any, key string) int {
	for i, r := range rules {
		if _, ok := r[key]; ok {
			return i
		}
	}
	return -1
}

func TestUnblockPinsCensoredServicesToProxy(t *testing.T) {
	o := Options{Mode: ModeSmart, UnblockServices: true}.Normalize()
	rules := o.RouteRules()

	r := ruleFor(rules, "domain_suffix")
	if r == nil {
		t.Fatal("no domain rule emitted")
	}
	if r["outbound"] != tagProxy {
		t.Fatalf("domain rule targets %v, want the proxy", r["outbound"])
	}
	for _, want := range []string{"youtube.com", "googlevideo.com", "discord.com", "discord.media"} {
		if !contains(suffixesOf(r), want) {
			t.Errorf("preset does not cover %q", want)
		}
	}
}

// The whole point of the preset: googlevideo.com resolves to an RU cache node,
// so if the geo rule ran first the video would be pinned direct and never load
// while the VPN reported itself connected.
func TestUnblockRuleComesBeforeTheGeoSplit(t *testing.T) {
	o := Options{Mode: ModeSmart, UnblockServices: true}.Normalize()
	rules := o.RouteRules()

	domainAt := indexOfRule(rules, "domain_suffix")
	geoAt := indexOfRule(rules, "rule_set")
	if domainAt < 0 || geoAt < 0 {
		t.Fatalf("expected both rules; domain=%d geo=%d", domainAt, geoAt)
	}
	if domainAt > geoAt {
		t.Fatalf("service rule at %d comes after the geo split at %d", domainAt, geoAt)
	}
}

// A tunnelled service must also resolve through the proxy resolver: the ISP
// resolver is exactly where a filtered answer for a censored name comes from.
func TestUnblockAlsoRoutesDNSThroughTheProxyResolver(t *testing.T) {
	o := Options{Mode: ModeSmart, UnblockServices: true}.Normalize()

	var found map[string]any
	for _, r := range o.dnsRules() {
		if s, ok := r["domain_suffix"].([]string); ok && contains(s, "googlevideo.com") {
			found = r
			break
		}
	}
	if found == nil {
		t.Fatal("no DNS rule for the preset domains")
	}
	if found["server"] != dnsRemoteTag {
		t.Fatalf("DNS server = %v, want the remote resolver", found["server"])
	}
}

func TestUnblockIsInertInDirectMode(t *testing.T) {
	o := Options{Mode: ModeDirect, UnblockServices: true}.Normalize()
	if r := ruleFor(o.RouteRules(), "domain_suffix"); r != nil {
		t.Fatalf("direct mode emitted a proxy pin: %v", r)
	}
}

func TestGamesPresetPinsLaunchersAndHelpers(t *testing.T) {
	o := Options{Mode: ModeSmart, GamesDirect: true}.Normalize()
	rules := o.RouteRules()

	r := ruleFor(rules, "process_name")
	if r == nil {
		t.Fatal("games preset emitted no process rule")
	}
	if r["outbound"] != tagDirect {
		t.Fatalf("games rule targets %v, want direct", r["outbound"])
	}
	apps, _ := r["process_name"].([]string)
	// The helpers are the entries a hand-built list forgets, and forgetting them
	// breaks the game more confusingly than forgetting the game itself.
	for _, want := range []string{"dota2.exe", "cs2.exe", "steamwebhelper.exe", "minecraftlauncher.exe"} {
		if !contains(apps, want) {
			t.Errorf("preset does not cover %q", want)
		}
	}
}

// process_name matches a bare file name, so a generic one in the preset takes
// every program that happens to be called that out of the tunnel. `java.exe` and
// `javaw.exe` are every JVM program on the machine — an IDE, a corporate client,
// a build tool — and `launcher.exe` is a name half the industry ships. A user who
// switched on "keep games direct" chose games, not "un-tunnel anything written in
// Java".
func TestGamesPresetClaimsNoGenericExecutableNames(t *testing.T) {
	for _, name := range []string{"java.exe", "javaw.exe", "launcher.exe"} {
		if contains(GameProcesses(), name) {
			t.Errorf("games preset claims the generic name %q", name)
		}
	}
}

// "Keep games direct" must not silently require the user to also discover and
// enable split tunnelling.
func TestGamesPresetWorksWithoutSplitModeOn(t *testing.T) {
	o := Options{Mode: ModeSmart, GamesDirect: true}.Normalize()
	if o.SplitMode == SplitExclude {
		t.Fatalf("test assumes split mode stays off, got %q", o.SplitMode)
	}
	if r := ruleFor(o.RouteRules(), "process_name"); r == nil {
		t.Fatal("no games rule with split tunnelling off")
	}
}

func TestGamesPresetMergesWithUserApps(t *testing.T) {
	o := Options{
		Mode:      ModeSmart,
		SplitMode: SplitExclude,
		// One name overlaps the preset: it must collapse, not appear twice.
		SplitApps:   []string{"MyGame.exe", "DOTA2.EXE"},
		GamesDirect: true,
	}.Normalize()

	apps, _ := ruleFor(o.RouteRules(), "process_name")["process_name"].([]string)
	if !contains(apps, "mygame.exe") {
		t.Error("user app dropped by the preset merge")
	}
	seen := 0
	for _, a := range apps {
		if a == "dota2.exe" {
			seen++
		}
	}
	if seen != 1 {
		t.Errorf("dota2.exe appears %d times, want exactly 1", seen)
	}
}

// The kill switch promises nothing leaves outside the tunnel. A preset that
// quietly exempted every game (or all voice UDP) would make that promise false
// without the user ever choosing it.
//
// Every path that pins traffic to the direct outbound has to honour it, not only
// the two presets that happened to check: the bypass hand-off, the bundled RU
// banking/government rule presets and the LAN bypass all send traffic out of the
// tunnel by exactly the same mechanism. The earlier version of this test armed
// only GamesDirect and VoiceDirect, so it passed while proving the guarantee for
// the two cases that already worked.
func TestPresetsYieldToTheKillSwitch(t *testing.T) {
	o := Options{
		Mode:            ModeSmart,
		KillSwitch:      true,
		GamesDirect:     true,
		VoiceDirect:     true,
		UnblockServices: true,
		ZapretActive:    true,
		ZapretCovered:   bundleCoverage,
		PresetRuBanking: true,
		PresetRuGov:     true,
		BypassLAN:       true,
	}.Normalize()
	rules := o.RouteRules()

	if r := ruleFor(rules, "process_name"); r != nil {
		t.Errorf("games preset emitted a direct rule under the kill switch: %v", r)
	}
	if r := ruleFor(rules, "port_range"); r != nil {
		t.Errorf("voice preset emitted a direct rule under the kill switch: %v", r)
	}
	if r := ruleFor(rules, "ip_is_private"); r != nil {
		t.Errorf("LAN bypass emitted a direct rule under the kill switch: %v", r)
	}
	if got := suffixesTo(rules, tagDirect); len(got) > 0 {
		t.Errorf("domains pinned direct under the kill switch: %v", got)
	}
}

// The DNS half leaks the same thing one step earlier: a name resolved by the
// ISP's resolver tells it who is being visited even when the connection itself
// never happens. Banking, government and bypass-covered names all mirror onto
// dns-direct, so the kill switch has to cut them there too.
func TestKillSwitchKeepsDomainLookupsOffTheDirectResolver(t *testing.T) {
	o := Options{
		Mode:            ModeSmart,
		KillSwitch:      true,
		UnblockServices: true,
		ZapretActive:    true,
		ZapretCovered:   bundleCoverage,
		PresetRuBanking: true,
		PresetRuGov:     true,
		RulesDirect:     []string{"example.com"},
	}.Normalize()

	rules, _ := o.DNS()["rules"].([]map[string]any)
	for _, r := range rules {
		if r["server"] != dnsDirectTag {
			continue
		}
		// The smart-mode geosite rule is the geo split, not a direct pin, and is
		// governed by the mode rather than by this guarantee.
		if _, isSuffix := r["domain_suffix"]; isSuffix {
			t.Errorf("names pinned to the direct resolver under the kill switch: %v", r)
		}
	}
}

// Direct mode tunnels nothing, so a "keep this direct" rule there is a no-op
// rather than a leak — the whole config is already direct. Collapsing the two
// gates into one helper must not start emitting rules in a mode that never had
// them.
func TestDirectModeStillEmitsNoDirectPins(t *testing.T) {
	o := Options{
		Mode:            ModeDirect,
		GamesDirect:     true,
		VoiceDirect:     true,
		UnblockServices: true,
		ZapretActive:    true,
		PresetRuBanking: true,
		PresetRuGov:     true,
		RulesDirect:     []string{"example.com"},
	}.Normalize()
	rules := o.RouteRules()

	if r := ruleFor(rules, "process_name"); r != nil {
		t.Errorf("games preset emitted a rule in direct mode: %v", r)
	}
	if r := ruleFor(rules, "port_range"); r != nil {
		t.Errorf("voice preset emitted a rule in direct mode: %v", r)
	}
	if got := suffixesTo(rules, tagDirect); len(got) > 0 {
		t.Errorf("domains pinned direct in direct mode: %v", got)
	}
}

// The kill switch cutting the direct pins must not take the proxy pins with it:
// a domain the user forced through the tunnel still has to be forced through the
// tunnel, and in smart mode an RU-resolving one would otherwise fall through to
// the geo rule and go direct — turning the kill switch into the leak it exists
// to prevent.
func TestKillSwitchKeepsProxyPinnedDomains(t *testing.T) {
	o := Options{
		Mode:            ModeSmart,
		KillSwitch:      true,
		UnblockServices: true,
		RulesProxy:      []string{"mail.example"},
	}.Normalize()

	proxied := suffixesTo(o.RouteRules(), tagProxy)
	for _, want := range []string{"mail.example", "youtube.com"} {
		if !contains(proxied, want) {
			t.Errorf("%q lost its proxy pin under the kill switch; got %v", want, proxied)
		}
	}

	rules, _ := o.DNS()["rules"].([]map[string]any)
	var remote []string
	for _, r := range rules {
		if r["server"] != dnsRemoteTag {
			continue
		}
		s, _ := r["domain_suffix"].([]string)
		remote = append(remote, s...)
	}
	if !contains(remote, "mail.example") {
		t.Errorf("proxy-pinned name lost its remote resolver under the kill switch; got %v", remote)
	}
}

// Apps the user typed into the exclude list themselves are an explicit,
// per-application choice — the distinction the presets' own comment draws when it
// says a preset must not exempt traffic "without the user ever choosing it". They
// stay direct under the kill switch; only the preset half of the merged list
// drops out.
func TestKillSwitchKeepsUserChosenExcludedApps(t *testing.T) {
	o := Options{
		Mode:        ModeSmart,
		KillSwitch:  true,
		SplitMode:   SplitExclude,
		SplitApps:   []string{"myapp.exe"},
		GamesDirect: true,
	}.Normalize()

	apps, _ := ruleFor(o.RouteRules(), "process_name")["process_name"].([]string)
	if !contains(apps, "myapp.exe") {
		t.Errorf("the user's own excluded app was dropped: %v", apps)
	}
	if contains(apps, "dota2.exe") {
		t.Errorf("the games preset rode into the exclude list under the kill switch: %v", apps)
	}
}

func TestVoiceDirectPinsRealtimeUDP(t *testing.T) {
	o := Options{Mode: ModeSmart, VoiceDirect: true}.Normalize()
	r := ruleFor(o.RouteRules(), "port_range")
	if r == nil {
		t.Fatal("voice preset emitted no rule")
	}
	if r["outbound"] != tagDirect {
		t.Fatalf("voice rule targets %v, want direct", r["outbound"])
	}
	if r["network"] != "udp" {
		t.Fatalf("voice rule network = %v, want udp only", r["network"])
	}
	ports, _ := r["port_range"].([]string)
	if len(ports) != 1 || !strings.HasPrefix(ports[0], "50000:") {
		t.Fatalf("port range = %v, want the high ephemeral range", ports)
	}
}

// A blanket "all UDP direct" would push QUIC (443) and DNS (53) out of the
// tunnel — uncensoring nothing while exposing plenty.
func TestVoiceDirectDoesNotCoverQUICOrDNS(t *testing.T) {
	o := Options{Mode: ModeSmart, VoiceDirect: true}.Normalize()
	ports, _ := ruleFor(o.RouteRules(), "port_range")["port_range"].([]string)
	for _, p := range ports {
		if strings.HasPrefix(p, "1:") || p == "0:65535" || strings.Contains(p, ":443") {
			t.Fatalf("range %q is too broad", p)
		}
	}
}

func TestPresetsOffByDefault(t *testing.T) {
	o := Options{Mode: ModeSmart}.Normalize()
	rules := o.RouteRules()
	if r := ruleFor(rules, "process_name"); r != nil {
		t.Errorf("games rule emitted without opting in: %v", r)
	}
	if r := ruleFor(rules, "port_range"); r != nil {
		t.Errorf("voice rule emitted without opting in: %v", r)
	}
	if r := ruleFor(rules, "domain_suffix"); r != nil {
		t.Errorf("service rule emitted without opting in: %v", r)
	}
}
