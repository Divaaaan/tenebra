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

// TestRuleSetDirRequiresTheGeodata: ruleSetDir returns the sing-box directory
// once the RU geodata is there, and withholds it until then. It is only a hint —
// the routing layer stats each file as it builds a config — but the hint has to
// point at a directory that actually holds the geodata, or nothing will.
func TestRuleSetDirRequiresTheGeodata(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "sing-box.exe")
	if err := os.WriteFile(bin, []byte("fake"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TENEBRA_SINGBOX", bin)

	// No .srs yet: must decline. Smart mode then routes like global rather than
	// naming a path sing-box cannot open.
	if got := ruleSetDir(); got != "" {
		t.Errorf("ruleSetDir with no .srs = %q, want empty", got)
	}

	// Half the geodata is still not a smart split, so the directory is withheld.
	if err := os.WriteFile(filepath.Join(dir, "geoip-ru.srs"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ruleSetDir(); got != "" {
		t.Errorf("ruleSetDir with only geoip-ru.srs = %q, want empty", got)
	}

	// Both RU sets present: the directory is taken. The ad blocklist is NOT
	// required here — it is referenced only when the user turns ad-blocking on, and
	// requiring it meant one absent optional file disqualified the directory and
	// took smart mode's geodata down with it for a feature that was switched off.
	if err := os.WriteFile(filepath.Join(dir, "geosite-ru.srs"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ruleSetDir(); got != dir {
		t.Errorf("ruleSetDir with the RU pair = %q, want %q", got, dir)
	}

	// And it keeps the directory once the blocklist arrives too.
	if err := os.WriteFile(filepath.Join(dir, "geosite-ads.srs"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ruleSetDir(); got != dir {
		t.Errorf("ruleSetDir with every .srs = %q, want %q", got, dir)
	}
}

// TestRuleSetDirNoEnv: with TENEBRA_SINGBOX unset there is no resources dir to
// probe, so ruleSetDir declines rather than falling back to the working
// directory.
func TestRuleSetDirNoEnv(t *testing.T) {
	t.Setenv("TENEBRA_SINGBOX", "")
	// On a platform whose search reaches system install directories (Linux), a
	// machine that already has Tenebra installed holds a complete bundle in one
	// of them, and finding it is the correct answer — there is no "nothing to
	// probe" case left to assert. Say so rather than fail on a real install.
	for _, dir := range ruleSetCandidates() {
		if hasRuleSets(dir) {
			t.Skipf("a system-wide rule-set bundle at %s leaves no empty case to observe", dir)
		}
	}
	if got := ruleSetDir(); got != "" {
		t.Errorf("ruleSetDir with no env = %q, want empty", got)
	}
}
