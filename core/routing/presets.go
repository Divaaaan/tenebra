package routing

import "sort"

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
// Inert in direct mode, where nothing is tunnelled and pinning a domain to the
// proxy would be a contradiction.
func (o Options) unblockActive() bool {
	return o.UnblockServices && o.Mode != ModeDirect
}

// proxySuffixesWithPresets merges the user's proxy-pinned domains with the
// blocked-services preset. The user's own entries are preserved; the merge is
// de-duplicated and sorted so the emitted rule is stable.
func (o Options) proxySuffixesWithPresets() []string {
	base := o.proxyRuleSuffixes()
	if !o.unblockActive() {
		return base
	}
	merged := make([]string, 0, len(base)+len(blockedServiceSuffixes))
	merged = append(merged, base...)
	merged = append(merged, blockedServiceSuffixes...)
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
