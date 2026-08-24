package routing

import (
	"sort"
	"strings"
)

// This file holds the user-defined routing rules and the bundled RU direct-rule
// presets. A rule is a domain suffix pinned to an outbound: RulesDirect keeps
// matching destinations on the direct outbound, RulesProxy sends them through the
// proxy. The presets are curated direct-rule lists a user opts into with a single
// toggle.
//
// Why the presets exist: some Russian banking and public-service sites reject
// connections that arrive from a foreign IP address — a proxy exit abroad looks
// like account fraud to them — so a user tunnelling everything through a foreign
// node loses access to their own bank or government account. Keeping those
// destinations on the direct outbound is a routing convenience for exactly that
// case; it is opt-in and off by default.

// Preset domain-suffix lists. Each entry is a domain suffix, so it also matches
// subdomains (online.sberbank.ru matches sberbank.ru). ASCII only — the config
// never carries an IDN/punycode label. The lists are deliberately short and
// conservative (the common apex domains, not an exhaustive registry); a user who
// needs more adds their own rule.
var (
	// presetRuBankingSuffixes are major Russian banks plus the national payment
	// system (Mir card / SBP). nspk.ru covers the SBP host (sbp.nspk.ru) as a
	// suffix, so it is not listed separately.
	presetRuBankingSuffixes = []string{
		"sberbank.ru",
		"sberbank.com",
		"vtb.ru",
		"tbank.ru",
		"tinkoff.ru",
		"alfabank.ru",
		"gazprombank.ru",
		"gpb.ru",
		"raiffeisen.ru",
		"psbank.ru",
		"sovcombank.ru",
		"rshb.ru",
		"mkb.ru",
		"rosbank.ru",
		"nspk.ru",
		"mironline.ru",
		"cbr.ru",
	}

	// presetRuGovSuffixes are Russian public-service and government domains.
	// gov.ru covers the many *.gov.ru agency subdomains (pension fund, etc.) as a
	// suffix, so they are not listed separately.
	presetRuGovSuffixes = []string{
		"gosuslugi.ru",
		"gov.ru",
		"mos.ru",
		"nalog.ru",
		"kremlin.ru",
		"government.ru",
		"mvd.ru",
		"rosreestr.ru",
		"edu.ru",
	}
)

// normalizeSuffixes lowercases, trims, de-duplicates and sorts domain suffixes so
// the emitted rules and the reported state stay stable regardless of input order
// or casing — the same shape normalizeApps gives executable names. domain_suffix
// matching in sing-box is case-insensitive, so lowercasing is safe and keeps the
// config deterministic. It does not validate the suffixes; ValidDomainSuffix (at
// the control boundary) and Options.Validate do that.
func normalizeSuffixes(suffixes []string) []string {
	if len(suffixes) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(suffixes))
	out := make([]string, 0, len(suffixes))
	for _, s := range suffixes {
		s = strings.ToLower(strings.TrimSpace(s))
		if s == "" {
			continue
		}
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil
	}
	sort.Strings(out)
	return out
}

// ValidDomainSuffix reports whether s is usable as a domain_suffix match: a
// non-empty, lowercase-normalizable name of ASCII letters, digits, dots and
// hyphens, not starting or ending with a dot or hyphen, and carrying at least one
// letter. It rejects anything with a scheme, slash, port, whitespace, '@' or
// other punctuation — none of which is a bare domain — so the daemon can refuse
// garbage before it reaches sing-box. Casing is tolerated (Normalize lowercases),
// so the check runs on the lowercased form; it mirrors lib/rules.ts on the client
// side.
func ValidDomainSuffix(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return false
	}
	if s[0] == '.' || s[0] == '-' || s[len(s)-1] == '.' || s[len(s)-1] == '-' {
		return false
	}
	hasLetter := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z':
			hasLetter = true
		case c >= '0' && c <= '9':
		case c == '.' || c == '-':
		default:
			return false
		}
	}
	return hasLetter
}

// rulesActive reports whether the proxy-direction domain rules should be
// emitted. They only make sense when the tunnel is in play: in direct mode
// nothing is tunnelled, so a "force through the tunnel" rule would re-tunnel
// traffic the user asked to send straight out. Gating on the mode matches the
// split layer, which likewise adds no proxy split in direct mode. Smart and
// global both keep the proxy as the base target, so the rules apply there.
//
// It deliberately does not ask directPinAllowed: the kill switch must not drop a
// proxy pin. Dropping one would leave the domain to the geo split, and in smart
// mode an RU-resolving domain the user forced through the tunnel would then go
// direct — the kill switch causing the leak it exists to prevent.
func (o Options) rulesActive() bool {
	return o.Mode != ModeDirect
}

// directRuleSuffixes returns the merged, normalized domain suffixes to pin to the
// direct outbound: the user's RulesDirect plus the suffixes of any enabled preset.
// They are merged into one list (and re-normalized) so overlapping user/preset
// entries collapse and a single domain_suffix rule covers them all. Empty (and nil
// where a direct pin is not allowed at all).
//
// This is a direct pin like the presets, so it asks directPinAllowed rather than
// rulesActive: PresetRuBanking and PresetRuGov carry the domains of Russian banks
// and public services, and under the kill switch they were both connecting and
// resolving outside the tunnel.
func (o Options) directRuleSuffixes() []string {
	if !o.directPinAllowed() {
		return nil
	}
	merged := make([]string, 0, len(o.RulesDirect)+len(presetRuBankingSuffixes)+len(presetRuGovSuffixes))
	merged = append(merged, o.RulesDirect...)
	if o.PresetRuBanking {
		merged = append(merged, presetRuBankingSuffixes...)
	}
	if o.PresetRuGov {
		merged = append(merged, presetRuGovSuffixes...)
	}
	return normalizeSuffixes(merged)
}

// proxyRuleSuffixes returns the normalized domain suffixes to pin to the proxy
// outbound (the user's RulesProxy). There is no preset for the proxy direction —
// the presets are direct-only. Nil in direct mode, where the rules are inert.
func (o Options) proxyRuleSuffixes() []string {
	if !o.rulesActive() {
		return nil
	}
	return normalizeSuffixes(o.RulesProxy)
}
