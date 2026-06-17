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
