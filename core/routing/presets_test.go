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
	for _, want := range []string{"dota2.exe", "cs2.exe", "steamwebhelper.exe", "javaw.exe"} {
		if !contains(apps, want) {
			t.Errorf("preset does not cover %q", want)
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
func TestPresetsYieldToTheKillSwitch(t *testing.T) {
	o := Options{
		Mode:        ModeSmart,
		KillSwitch:  true,
		GamesDirect: true,
		VoiceDirect: true,
	}.Normalize()
	rules := o.RouteRules()

	if r := ruleFor(rules, "process_name"); r != nil {
		t.Errorf("games preset emitted a direct rule under the kill switch: %v", r)
	}
	if r := ruleFor(rules, "port_range"); r != nil {
		t.Errorf("voice preset emitted a direct rule under the kill switch: %v", r)
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
