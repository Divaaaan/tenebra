package routing

import (
	"sort"
	"strings"
)

// Routing presets: the splits that almost every user of a censored network ends
// up assembling by hand, shipped so they are one switch instead of a dozen
// remembered executable names and port ranges.

// gameProcesses are the executables the GamesDirect preset pins to the direct
// outbound.
//
// The list includes launchers and helper processes, not just the games: those
// are exactly the entries a hand-built list forgets, and forgetting them breaks
// the game more confusingly than forgetting the game itself. `steamwebhelper.exe`
// renders the in-game overlay and the store, so tunnelling it stalls the overlay
// while the match itself is fine; `javaw.exe` is what actually talks to a
// Minecraft server; the Riot and Epic clients hold the session the game needs to
// even start.
//
// Names are matched case-insensitively on the executable file name by sing-box's
// process_name, so paths and casing do not matter.
var gameProcesses = []string{
	// Valve
	"steam.exe", "steamwebhelper.exe", "steamservice.exe", "steamerrorreporter.exe",
	"dota2.exe", "cs2.exe", "csgo.exe", "hl2.exe", "gmod.exe", "tf_win64.exe",
	// Facepunch / Unity-based survival
	"rustclient.exe",
	// Minecraft: the launcher spawns javaw, which is the process that connects
	"javaw.exe", "java.exe", "minecraftlauncher.exe", "minecraft.windows.exe",
	// Riot
	"riotclientservices.exe", "leagueoflegends.exe", "valorant.exe", "valorant-win64-shipping.exe",
	// Epic / Battle.net / EA / Ubisoft launchers
	"epicgameslauncher.exe", "battle.net.exe", "eadesktop.exe", "ealauncher.exe",
	"upc.exe", "ubisoftconnect.exe",
	// GOG / Rockstar
	"galaxyclient.exe", "launcher.exe", "rockstarservice.exe", "socialclubhelper.exe",
}

// GameProcesses returns a copy of the preset's executable names, normalized the
// same way user-supplied split apps are. Exported so the UI can show exactly
// what the switch will do rather than describing it vaguely.
func GameProcesses() []string {
	out := make([]string, len(gameProcesses))
	copy(out, gameProcesses)
	sort.Strings(out)
	return out
}

// blockedServiceSuffixes are the domains the UnblockServices preset pins to the
// proxy.
//
// Why an explicit list instead of trusting smart mode: smart mode sends
// RU-resolved addresses direct, and several of these services answer from
// *inside* Russia. `googlevideo.com` — where YouTube video actually streams from
// — commonly resolves to a Google Global Cache node hosted at the ISP, so the
// geo rule pins it direct and the video never loads while the VPN is on and
// looks connected. Discord's media and CDN hosts behave the same way. Matching
// by domain, before the geo split runs, is the only ordering that survives this.
//
// The list covers the whole domain family per service, not just the front page:
// YouTube alone spreads across youtube.com (page), googlevideo.com (video),
// ytimg.com (thumbnails) and youtubei.googleapis.com (the API the app calls) —
// pinning only youtube.com yields a page that loads and a video that spins.
var blockedServiceSuffixes = []string{
	// YouTube / YouTube Music
	"youtube.com", "youtu.be", "googlevideo.com", "ytimg.com", "yt3.ggpht.com",
	"youtubei.googleapis.com", "music.youtube.com",
	// Discord (signalling, CDN, media)
	"discord.com", "discordapp.com", "discordapp.net", "discord.gg", "discord.media",
	"discordcdn.com", "discordstatus.com",
	// Meta
	"instagram.com", "cdninstagram.com", "facebook.com", "fbcdn.net", "whatsapp.com",
	// X / Twitter
	"twitter.com", "x.com", "twimg.com",
	// AI services. These are not merely slow without the tunnel — the direct path
	// is actively poisoned: api.anthropic.com answers a forged 403 in ~40ms on the
	// author's ISP, and the genuine response only arrives through the proxy. A
	// forged answer is worse than a timeout, because every client above treats it
	// as a real refusal from the service and reports an auth problem that does not
	// exist. Developer tooling that talks to these APIs breaks in a way that looks
	// like the tool's fault.
	"anthropic.com", "claude.ai", "claudeusercontent.com",
	"openai.com", "chatgpt.com", "oaistatic.com",
	// Other commonly-blocked
	"soundcloud.com", "spotify.com", "scdn.co", "linkedin.com", "licdn.com",
	"medium.com", "patreon.com", "signal.org",
}

// BlockedServiceSuffixes returns a copy of the preset's domains, so the UI can
// show exactly what the switch routes rather than describing it vaguely.
func BlockedServiceSuffixes() []string {
	out := make([]string, len(blockedServiceSuffixes))
	copy(out, blockedServiceSuffixes)
	sort.Strings(out)
	return out
}

// unblockActive reports whether the blocked-services preset should emit rules.
//
// Inert in direct mode, where nothing is tunnelled and pinning a domain to the
// proxy would be a contradiction. It stays active while the DPI bypass runs:
// the bypass covers only part of the preset (its host lists carry YouTube,
// Google and Discord and nothing else), so the rest still needs the tunnel.
// zapretCoveredSuffixes/proxySuffixesWithPresets split the list between the two
// paths rather than handing all of it to one.
func (o Options) unblockActive() bool {
	return o.UnblockServices && o.Mode != ModeDirect
}

// defaultZapretCovered is the coverage assumed when the bundle's host lists
// could not be read.
//
// It is deliberately the narrow, certain part: every published build of the
// bypass is built around YouTube and Discord, so those are safe to route
// direct even without reading the lists. Guessing wider would be the dangerous
// direction — a service the bypass does not touch, pinned direct, is not slow
// but broken.
var defaultZapretCovered = []string{
	"youtube.com", "youtu.be", "googlevideo.com", "ytimg.com", "ggpht.com",
	"youtubei.googleapis.com",
	"discord.com", "discordapp.com", "discordapp.net", "discord.gg",
	"discord.media", "discordcdn.com", "discordstatus.com",
}

// coverage returns the domains the running bypass actually acts on.
func (o Options) coverage() []string {
	if len(o.ZapretCovered) > 0 {
		return o.ZapretCovered
	}
	return defaultZapretCovered
}

// coveredByZapret reports whether domain (or a parent of it) appears in the
// bypass's coverage. Parent matching is what makes `music.youtube.com` count as
// covered by a list that only names `youtube.com`.
func coveredByZapret(covered []string, domain string) bool {
	for _, c := range covered {
		if domain == c || strings.HasSuffix(domain, "."+c) {
			return true
		}
	}
	return false
}

// zapretDirectSuffixes are the preset services the running bypass covers, handed
// to the direct path instead of the tunnel.
//
// With the bypass up these connect directly at the ISP's own latency, so
// tunnelling them would pay the whole round trip for nothing — 239ms against
// 9ms on the author's network. The intersection with the bundle's host lists is
// the load-bearing part: the preset lists services the bypass has never heard of
// (Anthropic, OpenAI, Instagram, X, Spotify), and routing those direct because
// "the bypass is on" does not make them slow, it makes them fail — on this ISP
// api.anthropic.com answers a forged 403 on the direct path, so every tool
// talking to it reports an authentication error that does not exist.
func (o Options) zapretDirectSuffixes() []string {
	if !o.ZapretActive || o.Mode == ModeDirect {
		return nil
	}
	covered := o.coverage()
	out := make([]string, 0, len(blockedServiceSuffixes))
	for _, s := range blockedServiceSuffixes {
		if coveredByZapret(covered, s) {
			out = append(out, s)
		}
	}
	return normalizeSuffixes(out)
}

// proxySuffixesWithPresets merges the user's proxy-pinned domains with the
// blocked-services preset. The user's own entries are preserved; the merge is
// de-duplicated and sorted so the emitted rule is stable.
//
// While the bypass runs, the services it covers are removed from this list —
// they are pinned direct by zapretDirectSuffixes instead, and emitting both
// would be two rules claiming the same domain for opposite outbounds. What the
// bypass does not cover stays here, in the tunnel, which is the whole point of
// running the two together.
func (o Options) proxySuffixesWithPresets() []string {
	base := o.proxyRuleSuffixes()
	if !o.unblockActive() {
		return base
	}
	merged := make([]string, 0, len(base)+len(blockedServiceSuffixes))
	merged = append(merged, base...)
	if o.ZapretActive {
		covered := o.coverage()
		for _, s := range blockedServiceSuffixes {
			if !coveredByZapret(covered, s) {
				merged = append(merged, s)
			}
		}
	} else {
		merged = append(merged, blockedServiceSuffixes...)
	}
	// normalizeSuffixes lowercases, trims, de-duplicates and sorts, so the merge
	// needs no bookkeeping of its own and stays consistent with user-entered rules.
	return normalizeSuffixes(merged)
}

// voicePortRange is the UDP range real-time media uses.
//
// Discord's voice servers hand out ports in the high ephemeral range, and the
// same range carries most WebRTC and game traffic — which is the point: these
// are the flows that are never censored and are ruined by a detour. The range
// deliberately starts at 50000 rather than covering all of UDP, so DNS (53),
// QUIC/HTTP3 (443) and WireGuard-style tunnels keep their normal routing; a
// blanket "all UDP direct" would push QUIC web traffic out of the tunnel and
// silently un-censor nothing while exposing plenty.
const voicePortRange = "50000:65535"

// splitAppsWithPresets merges the user's split apps with any enabled preset,
// lowercased and de-duplicated, preserving the user's own entries.
func (o Options) splitAppsWithPresets() []string {
	if !o.gamesDirectActive() {
		return o.SplitApps
	}
	merged := make([]string, 0, len(o.SplitApps)+len(gameProcesses))
	merged = append(merged, o.SplitApps...)
	merged = append(merged, gameProcesses...)
	// normalizeApps lowercases, trims, de-duplicates and sorts — the same
	// treatment user-entered app names get, so a manually added entry that also
	// appears in the preset collapses to one instead of emitting twice.
	return normalizeApps(merged)
}

// gamesDirectActive reports whether the games preset should emit a rule.
//
// Off in direct mode (nothing is tunnelled, so pinning apps to direct is a
// no-op) and off under the kill switch, whose guarantee is that no traffic
// leaves outside the tunnel — a preset that silently exempted every game would
// make that guarantee false without the user ever choosing it.
func (o Options) gamesDirectActive() bool {
	return o.GamesDirect && o.Mode != ModeDirect && !o.KillSwitch
}

// voiceDirectActive reports whether the real-time UDP rule should be emitted.
// Same reasoning as gamesDirectActive.
func (o Options) voiceDirectActive() bool {
	return o.VoiceDirect && o.Mode != ModeDirect && !o.KillSwitch
}
