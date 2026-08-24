package control

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestMigrateV1ClearsOnlyTheOutwardPresets: a v1 file (0.5.0's format) is walked
// forward to the current version at load time. The two outward-routing presets
// 0.5.0 forced on are cleared — they were a default the user could not see or
// change, not a choice — and every other field the file carries, including
// unblock-services, is a genuine user choice and must survive verbatim.
func TestMigrateV1ClearsOnlyTheOutwardPresets(t *testing.T) {
	dir := t.TempDir()
	// A rich v1 file: both outward presets on (as 0.5.0 wrote them) plus a spread
	// of real user choices across the other fields.
	v1 := []byte(`{
		"version":1,
		"preset_games_direct":true,
		"preset_voice_direct":true,
		"preset_unblock_services":false,
		"split_mode":"exclude",
		"split_apps":["chrome.exe","steam.exe"],
		"routing_mode":"global",
		"kill_switch":true,
		"tls_fragment":true,
		"tun_stack":"gvisor",
		"proxy_mode":"tun",
		"proxy_port":1080,
		"autoconnect":true,
		"ad_block":true,
		"dns_remote":"tls://9.9.9.9",
		"dns_direct":"https://77.88.8.8/dns-query",
		"rules_direct":["bank.example"],
		"rules_proxy":["news.example"],
		"preset_ru_banking":true,
		"preset_ru_gov":true,
		"last_profile":"prof-1"
	}`)
	if err := os.WriteFile(filepath.Join(dir, settingsFile), v1, 0o644); err != nil {
		t.Fatalf("seed v1 file: %v", err)
	}

	got := settingsAt(t, dir).Load()

	if got.Version != settingsVersion {
		t.Errorf("version after migration = %d, want %d", got.Version, settingsVersion)
	}
	// The two outward presets are cleared to an explicit false.
	if got.PresetGamesDirect == nil || *got.PresetGamesDirect {
		t.Errorf("preset_games_direct = %v, want explicit false", ptrStr(got.PresetGamesDirect))
	}
	if got.PresetVoiceDirect == nil || *got.PresetVoiceDirect {
		t.Errorf("preset_voice_direct = %v, want explicit false", ptrStr(got.PresetVoiceDirect))
	}
	// unblock-services and every other field are the user's and must be untouched.
	if got.PresetUnblockServices == nil || *got.PresetUnblockServices {
		t.Errorf("preset_unblock_services = %v, want the stored false", ptrStr(got.PresetUnblockServices))
	}
	if got.SplitMode != "exclude" || !reflect.DeepEqual(got.SplitApps, []string{"chrome.exe", "steam.exe"}) {
		t.Errorf("split config changed: mode=%q apps=%v", got.SplitMode, got.SplitApps)
	}
	if got.RoutingMode != "global" {
		t.Errorf("routing_mode = %q, want global", got.RoutingMode)
	}
	if !got.KillSwitch || !got.TLSFragment || !got.AdBlock || !got.Autoconnect {
		t.Errorf("a boolean choice was dropped: kill=%v tls=%v adblock=%v autoconnect=%v",
			got.KillSwitch, got.TLSFragment, got.AdBlock, got.Autoconnect)
	}
	if got.TunStack != "gvisor" || got.ProxyMode != "tun" || got.ProxyPort != 1080 {
		t.Errorf("a stack/proxy choice changed: stack=%q mode=%q port=%d",
			got.TunStack, got.ProxyMode, got.ProxyPort)
	}
	if got.DNSRemote != "tls://9.9.9.9" || got.DNSDirect != "https://77.88.8.8/dns-query" {
		t.Errorf("a resolver changed: remote=%q direct=%q", got.DNSRemote, got.DNSDirect)
	}
	if !reflect.DeepEqual(got.RulesDirect, []string{"bank.example"}) ||
		!reflect.DeepEqual(got.RulesProxy, []string{"news.example"}) {
		t.Errorf("a custom rule changed: direct=%v proxy=%v", got.RulesDirect, got.RulesProxy)
	}
	if !got.PresetRuBanking || !got.PresetRuGov {
		t.Errorf("an RU rule preset was dropped: banking=%v gov=%v",
			got.PresetRuBanking, got.PresetRuGov)
	}
	if got.LastProfile != "prof-1" {
		t.Errorf("last_profile = %q, want prof-1", got.LastProfile)
	}
}

// TestMigrateV1IsStableAfterSave: the migration is a one-time reset, not a
// standing override. Once a migrated v1 file is written back it is at v2, and a
// v2 file passes through untouched — so a preset the user turns on afterwards is
// not re-cleared on the next load.
func TestMigrateV1IsStableAfterSave(t *testing.T) {
	dir := t.TempDir()
	v1 := []byte(`{"version":1,"preset_games_direct":true,"preset_voice_direct":true}`)
	if err := os.WriteFile(filepath.Join(dir, settingsFile), v1, 0o644); err != nil {
		t.Fatalf("seed v1 file: %v", err)
	}

	store := settingsAt(t, dir)
	migrated := store.Load()
	if migrated.Version != settingsVersion {
		t.Fatalf("first load version = %d, want %d", migrated.Version, settingsVersion)
	}

	// The user turns voice-direct back on and it is persisted (now at v2).
	on := true
	migrated.PresetVoiceDirect = &on
	if err := store.Save(migrated); err != nil {
		t.Fatalf("save migrated settings: %v", err)
	}

	// The next load must honour that v2 choice rather than re-running the reset.
	reloaded := settingsAt(t, dir).Load()
	if reloaded.Version != settingsVersion {
		t.Errorf("reloaded version = %d, want %d", reloaded.Version, settingsVersion)
	}
	if reloaded.PresetVoiceDirect == nil || !*reloaded.PresetVoiceDirect {
		t.Errorf("a v2 choice was re-cleared: voice = %v", ptrStr(reloaded.PresetVoiceDirect))
	}
	if reloaded.PresetGamesDirect == nil || *reloaded.PresetGamesDirect {
		t.Errorf("games-direct = %v, want the migrated false to persist", ptrStr(reloaded.PresetGamesDirect))
	}
}

// TestMigratePreVersioningFileIsDropped: a file with no version field decodes to
// version 0, a layout this reader never wrote. It is treated as no settings, the
// same as a future version, so an unrecognised layout is never guessed at.
func TestMigratePreVersioningFileIsDropped(t *testing.T) {
	dir := t.TempDir()
	// No "version" key at all; carries fields, but the version is unknown (0).
	pre := []byte(`{"split_mode":"exclude","split_apps":["x.exe"],"kill_switch":true}`)
	if err := os.WriteFile(filepath.Join(dir, settingsFile), pre, 0o644); err != nil {
		t.Fatalf("seed pre-versioning file: %v", err)
	}

	got := settingsAt(t, dir).Load()
	if !reflect.DeepEqual(got, persistedSettings{}) {
		t.Errorf("a version-0 file was not dropped: %+v", got)
	}
}

// ptrStr renders a *bool for a test failure message: "nil", "true" or "false".
func ptrStr(b *bool) string {
	if b == nil {
		return "nil"
	}
	if *b {
		return "true"
	}
	return "false"
}
