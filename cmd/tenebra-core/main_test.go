package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestConfigDirOverride verifies that an explicit TENEBRA_CONFIG_DIR wins and is
// returned verbatim.
func TestConfigDirOverride(t *testing.T) {
	want := filepath.Join(t.TempDir(), "store")
	t.Setenv("TENEBRA_CONFIG_DIR", want)

	if got := configDir(); got != want {
		t.Errorf("configDir() = %q, want %q", got, want)
	}
}

// TestConfigDirDefault verifies that with no override set, configDir falls back
// to the per-user config location with a "tenebra" suffix, mirroring the source.
func TestConfigDirDefault(t *testing.T) {
	// Setting to "" exercises the unset path: configDir checks dir != "".
	t.Setenv("TENEBRA_CONFIG_DIR", "")

	base, err := os.UserConfigDir()
	if err != nil {
		base = "."
	}
	want := filepath.Join(base, "tenebra")

	if got := configDir(); got != want {
		t.Errorf("configDir() = %q, want %q", got, want)
	}
}

// TestRuleSetDirRequiresAllFiles: ruleSetDir returns the sing-box directory only
// when every bundled rule-set is present there, so an incomplete install falls
// back to remote download instead of pointing sing-box at a missing path.
func TestRuleSetDirRequiresAllFiles(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "sing-box.exe")
	if err := os.WriteFile(bin, []byte("fake"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TENEBRA_SINGBOX", bin)

	// No .srs yet: must decline (empty), keeping the remote fallback.
	if got := ruleSetDir(); got != "" {
		t.Errorf("ruleSetDir with no .srs = %q, want empty", got)
	}

	// A partial bundle still declines, since a missing path would make sing-box
	// FATAL: write the files one at a time and confirm the dir is withheld until
	// every required rule-set (the two RU sets plus the ad blocklist) is present.
	partial := []string{"geoip-ru.srs", "geosite-ru.srs"}
	for _, f := range partial {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := ruleSetDir(); got != "" {
			t.Errorf("ruleSetDir with %s present but not all files = %q, want empty", f, got)
		}
	}

	// The final required file present: returns the directory holding the binary.
	if err := os.WriteFile(filepath.Join(dir, "geosite-ads.srs"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ruleSetDir(); got != dir {
		t.Errorf("ruleSetDir with every .srs = %q, want %q", got, dir)
	}
}

// TestRuleSetDirNoEnv: with TENEBRA_SINGBOX unset there is no resources dir to
// probe, so ruleSetDir declines.
func TestRuleSetDirNoEnv(t *testing.T) {
	t.Setenv("TENEBRA_SINGBOX", "")
	if got := ruleSetDir(); got != "" {
		t.Errorf("ruleSetDir with no env = %q, want empty", got)
	}
}
