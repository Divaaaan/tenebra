package routing

import (
	"encoding/json"
	"reflect"
	"testing"
)

// suffixRuleIndex returns the index of the first rule whose domain_suffix match
// targets outbound, or -1. It inspects the pre-JSON []string form the builder
// emits.
func suffixRuleIndex(rules []map[string]any, outbound string) int {
	for i, r := range rules {
		if _, ok := r["domain_suffix"]; !ok {
			continue
		}
		if r["outbound"] == outbound {
			return i
		}
	}
	return -1
}

// suffixRuleSet returns the domain_suffix slice of the first rule targeting
// outbound, or nil.
func suffixRuleSet(rules []map[string]any, outbound string) []string {
	i := suffixRuleIndex(rules, outbound)
	if i < 0 {
		return nil
	}
	s, _ := rules[i]["domain_suffix"].([]string)
	return s
}

func TestRulesNormalizeLowercasesDedupesSorts(t *testing.T) {
	got := Options{
		Mode:        ModeSmart,
		RulesDirect: []string{"Sberbank.ru", "  vtb.ru ", "SBERBANK.RU", "", "alfabank.ru"},
		RulesProxy:  []string{"Example.com", "example.com"},
	}.Normalize()

	wantDirect := []string{"alfabank.ru", "sberbank.ru", "vtb.ru"}
	if !reflect.DeepEqual(got.RulesDirect, wantDirect) {
		t.Errorf("normalized direct = %v, want %v", got.RulesDirect, wantDirect)
	}
	wantProxy := []string{"example.com"}
	if !reflect.DeepEqual(got.RulesProxy, wantProxy) {
		t.Errorf("normalized proxy = %v, want %v", got.RulesProxy, wantProxy)
	}
}

func TestRulesNormalizeEmptyToNil(t *testing.T) {
	got := Options{Mode: ModeSmart, RulesDirect: []string{"  ", ""}}.Normalize()
	if got.RulesDirect != nil {
		t.Errorf("all-blank direct rules = %v, want nil", got.RulesDirect)
	}
	if got.RulesProxy != nil {
		t.Errorf("absent proxy rules = %v, want nil", got.RulesProxy)
	}
}

func TestValidDomainSuffix(t *testing.T) {
	valid := []string{
		"example.com",
		"sberbank.ru",
		"a.b.c.example.co.uk",
		"xn--90a3ac", // already-ASCII punycode label is allowed (letters+digits)
		"my-host.example",
		"host1.example2.com",
		"localhost",
		"Sberbank.RU", // normalizable to lowercase
	}
	for _, s := range valid {
		if !ValidDomainSuffix(s) {
			t.Errorf("ValidDomainSuffix(%q) = false, want true", s)
		}
	}

	invalid := []string{
		"",
		"   ",
		".com",          // leading dot
		"com.",          // trailing dot
		"-lead.example", // leading hyphen
		"example.com-",  // trailing hyphen
		"https://x.com", // scheme (slash + colon)
		"x.com/path",    // slash
		"x.com:443",     // port
		"user@x.com",    // at-sign
		"has space.com", // whitespace
		"123.456",       // no letter
		"пример.рф",     // non-ASCII
	}
	for _, s := range invalid {
		if ValidDomainSuffix(s) {
			t.Errorf("ValidDomainSuffix(%q) = true, want false", s)
		}
	}
}

// TestPresetSuffixesAreValid guards the hardcoded preset lists against a typo
// that would slip an unroutable suffix into the config.
func TestPresetSuffixesAreValid(t *testing.T) {
	for _, s := range presetRuBankingSuffixes {
		if !ValidDomainSuffix(s) {
			t.Errorf("banking preset suffix %q is not a valid domain suffix", s)
		}
	}
	for _, s := range presetRuGovSuffixes {
		if !ValidDomainSuffix(s) {
			t.Errorf("gov preset suffix %q is not a valid domain suffix", s)
		}
	}
}

func TestRulesValidate(t *testing.T) {
	base := Options{Mode: ModeSmart, DNSRemote: "a", DNSDirect: "b"}
	ok := base
	ok.RulesDirect = []string{"sberbank.ru"}
	ok.RulesProxy = []string{"example.com"}
	if err := ok.Validate(); err != nil {
		t.Errorf("valid rules rejected: %v", err)
	}

	badDirect := base
	badDirect.RulesDirect = []string{"https://nope"}
	if err := badDirect.Validate(); err == nil {
		t.Error("invalid direct suffix accepted")
	}

	badProxy := base
	badProxy.RulesProxy = []string{"x.com:443"}
	if err := badProxy.Validate(); err == nil {
		t.Error("invalid proxy suffix accepted")
	}
}

// TestRulesBetweenSplitAndGeo locks the emission order: the custom rules sit
// after the per-app split (process_name) and before the geo split (rule_set), so
// an app rule still wins and a user rule beats the RU geo preset.
func TestRulesBetweenSplitAndGeo(t *testing.T) {
	opts := (Options{
		Mode:        ModeSmart,
		SplitMode:   SplitExclude,
		SplitApps:   []string{"chrome.exe"},
		RulesDirect: []string{"sberbank.ru"},
		RulesProxy:  []string{"example.com"},
	}).Normalize()
	rules := opts.RouteRules()

	procIdx, geoIdx := -1, -1
	for i, r := range rules {
		if _, ok := r["process_name"]; ok {
			procIdx = i
		}
		if _, ok := r["rule_set"]; ok {
			geoIdx = i
		}
	}
	directIdx := suffixRuleIndex(rules, tagDirect)
	proxyIdx := suffixRuleIndex(rules, tagProxy)

	if procIdx < 0 || geoIdx < 0 || directIdx < 0 || proxyIdx < 0 {
		t.Fatalf("missing an expected rule; rules=%v", rules)
	}
	if !(procIdx < directIdx && procIdx < proxyIdx) {
		t.Errorf("custom rules (direct %d, proxy %d) must come after the process_name split (%d)", directIdx, proxyIdx, procIdx)
	}
	if !(directIdx < geoIdx && proxyIdx < geoIdx) {
		t.Errorf("custom rules (direct %d, proxy %d) must come before the geo split (%d)", directIdx, proxyIdx, geoIdx)
	}
	// Direct before proxy, matching the documented order.
	if directIdx > proxyIdx {
		t.Errorf("direct rule (%d) must come before proxy rule (%d)", directIdx, proxyIdx)
	}
	if got := suffixRuleSet(rules, tagDirect); !reflect.DeepEqual(got, []string{"sberbank.ru"}) {
		t.Errorf("direct suffixes = %v, want [sberbank.ru]", got)
	}
	if got := suffixRuleSet(rules, tagProxy); !reflect.DeepEqual(got, []string{"example.com"}) {
		t.Errorf("proxy suffixes = %v, want [example.com]", got)
	}
}

// TestRulesPresetsEmittedAsDirect: the presets contribute to the direct rule and
// merge with the user's own direct suffixes into one sorted, de-duplicated list.
func TestRulesPresetsEmittedAsDirect(t *testing.T) {
	opts := (Options{
		Mode:            ModeSmart,
		RulesDirect:     []string{"sberbank.ru", "myown.example"}, // sberbank.ru also in the preset
		PresetRuBanking: true,
		PresetRuGov:     true,
	}).Normalize()
	got := suffixRuleSet(opts.RouteRules(), tagDirect)

	// Every preset suffix plus the user's extra must be present, de-duplicated.
	want := map[string]bool{"myown.example": true}
	for _, s := range presetRuBankingSuffixes {
		want[s] = true
	}
	for _, s := range presetRuGovSuffixes {
		want[s] = true
	}
	if len(got) != len(want) {
		t.Errorf("merged direct suffix count = %d, want %d (deduped); got=%v", len(got), len(want), got)
	}
	seen := map[string]int{}
	for _, s := range got {
		seen[s]++
		if !want[s] {
			t.Errorf("unexpected suffix %q in merged direct rule", s)
		}
	}
	if seen["sberbank.ru"] != 1 {
		t.Errorf("sberbank.ru appears %d times, want 1 (user+preset deduped)", seen["sberbank.ru"])
	}
	// The presets never leak into the proxy direction.
	if idx := suffixRuleIndex(opts.RouteRules(), tagProxy); idx != -1 {
		t.Errorf("presets must not emit a proxy rule; found at %d", idx)
	}
}

// TestRulesPresetsOnlyBanking: enabling only one preset emits only its list.
func TestRulesPresetsOnlyBanking(t *testing.T) {
	opts := (Options{Mode: ModeGlobal, PresetRuBanking: true}).Normalize()
	got := suffixRuleSet(opts.RouteRules(), tagDirect)
	if len(got) != len(presetRuBankingSuffixes) {
		t.Errorf("banking-only direct count = %d, want %d", len(got), len(presetRuBankingSuffixes))
	}
	// A gov-only suffix must be absent.
	for _, s := range got {
		if s == "gosuslugi.ru" {
			t.Error("banking-only preset leaked a gov suffix")
		}
	}
}

// TestRulesNotEmittedInDirectMode: direct mode tunnels nothing, so no custom or
// preset rule is emitted — a "keep direct" rule is a no-op and a "force proxy"
// rule would re-tunnel what the user asked to send straight out.
func TestRulesNotEmittedInDirectMode(t *testing.T) {
	opts := (Options{
		Mode:            ModeDirect,
		RulesDirect:     []string{"sberbank.ru"},
		RulesProxy:      []string{"example.com"},
		PresetRuBanking: true,
		PresetRuGov:     true,
	}).Normalize()

	rules := opts.RouteRules()
	if idx := suffixRuleIndex(rules, tagDirect); idx != -1 {
		t.Errorf("direct mode emitted a direct rule at %d; rules=%v", idx, rules)
	}
	if idx := suffixRuleIndex(rules, tagProxy); idx != -1 {
		t.Errorf("direct mode emitted a proxy rule at %d; rules=%v", idx, rules)
	}
	// The DNS mirror is likewise absent.
	for _, r := range opts.dnsRules() {
		if _, ok := r["domain_suffix"]; ok {
			t.Errorf("direct mode emitted a domain_suffix dns rule: %v", r)
		}
	}
}

// TestRulesEmittedInGlobalMode: global keeps the proxy as the base target, so the
// rules apply — a direct rule is the only way to carve a destination out of an
// otherwise-everything tunnel.
func TestRulesEmittedInGlobalMode(t *testing.T) {
	opts := (Options{
		Mode:        ModeGlobal,
		RulesDirect: []string{"sberbank.ru"},
		RulesProxy:  []string{"example.com"},
	}).Normalize()
	rules := opts.RouteRules()
	if got := suffixRuleSet(rules, tagDirect); !reflect.DeepEqual(got, []string{"sberbank.ru"}) {
		t.Errorf("global direct suffixes = %v, want [sberbank.ru]", got)
	}
	if got := suffixRuleSet(rules, tagProxy); !reflect.DeepEqual(got, []string{"example.com"}) {
		t.Errorf("global proxy suffixes = %v, want [example.com]", got)
	}
}

// TestRulesDNSMirror: each route rule has a mirrored DNS rule sending the same
// domains to the matching resolver (direct→dns-direct, proxy→dns-remote), and the
// user rules precede the smart RU rule.
func TestRulesDNSMirror(t *testing.T) {
	opts := (Options{
		Mode:        ModeSmart,
		RulesDirect: []string{"sberbank.ru"},
		RulesProxy:  []string{"example.com"},
	}).Normalize()
	rules := opts.dnsRules()

	directIdx, proxyIdx, ruIdx := -1, -1, -1
	for i, r := range rules {
		if suf, ok := r["domain_suffix"].([]string); ok {
			switch r["server"] {
			case dnsDirectTag:
				if reflect.DeepEqual(suf, []string{"sberbank.ru"}) {
					directIdx = i
				}
			case dnsRemoteTag:
				if reflect.DeepEqual(suf, []string{"example.com"}) {
					proxyIdx = i
				}
			}
		}
		if _, ok := r["rule_set"]; ok && r["server"] == dnsDirectTag {
			ruIdx = i
		}
	}
	if directIdx < 0 {
		t.Errorf("missing direct DNS mirror (sberbank.ru -> dns-direct); rules=%v", rules)
	}
	if proxyIdx < 0 {
		t.Errorf("missing proxy DNS mirror (example.com -> dns-remote); rules=%v", rules)
	}
	if ruIdx < 0 {
		t.Fatalf("smart RU dns rule missing; rules=%v", rules)
	}
	if directIdx > ruIdx || proxyIdx > ruIdx {
		t.Errorf("user DNS rules (direct %d, proxy %d) must precede the RU rule (%d)", directIdx, proxyIdx, ruIdx)
	}
}

// TestRulesDefaultDomainResolverSurvives: the whole point of a route block is
// still valid — default_domain_resolver must remain present with the rules added
// (sing-box 1.13 FATALs without it).
func TestRulesDefaultDomainResolverSurvives(t *testing.T) {
	opts := (Options{
		Mode:            ModeSmart,
		RulesDirect:     []string{"sberbank.ru"},
		PresetRuBanking: true,
	}).Normalize()
	res := opts.DefaultDomainResolver()
	if res["server"] != dnsDirectTag {
		t.Errorf("default_domain_resolver = %v, want server %s", res, dnsDirectTag)
	}
}

// TestRulesMarshal guards against emitting anything encoding/json chokes on.
func TestRulesMarshal(t *testing.T) {
	opts := (Options{
		Mode:            ModeSmart,
		RulesDirect:     []string{"sberbank.ru"},
		RulesProxy:      []string{"example.com"},
		PresetRuBanking: true,
		PresetRuGov:     true,
	}).Normalize()
	if _, err := json.Marshal(opts.RouteRules()); err != nil {
		t.Fatalf("route rules do not marshal: %v", err)
	}
	if _, err := json.Marshal(opts.DNS()); err != nil {
		t.Fatalf("dns block does not marshal: %v", err)
	}
}
