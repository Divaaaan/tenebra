//go:build linux

package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// emptyPATH points PATH at an empty directory for the duration of a test, so the
// last step of the sing-box search (a lookup on PATH) is decided by the test
// rather than by whatever the machine running it happens to have installed.
func emptyPATH(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
}

// requireNoSystemInstall skips a "nothing is installed" assertion on a machine
// where Tenebra genuinely is installed system-wide. The absolute backstop in the
// search order is not injectable — it is the point of the backstop — so a real
// install legitimately answers, and calling that a failure would only teach
// contributors to ignore a red test.
func requireNoSystemInstall(t *testing.T) {
	t.Helper()
	for _, dir := range []string{"/usr/lib/tenebra", "/usr/libexec/tenebra", "/usr/share/tenebra"} {
		p := filepath.Join(dir, "sing-box")
		if _, err := os.Stat(p); err == nil {
			t.Skipf("a system-wide sing-box at %s leaves no miss to observe", p)
		}
	}
}

// TestFindSingbox walks the layouts the daemon resolves sing-box against on
// Linux: none present (decline), beside the executable (a self-contained
// install directory, an unpacked AppImage, or a dev checkout), and the private
// helper directories a distribution package uses when the launcher itself lives
// in <prefix>/bin.
func TestFindSingbox(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		emptyPATH(t)
		requireNoSystemInstall(t)
		if got := findSingbox(t.TempDir()); got != "" {
			t.Errorf("findSingbox on empty dir = %q, want empty", got)
		}
	})

	t.Run("flat", func(t *testing.T) {
		emptyPATH(t)
		dir := t.TempDir()
		want := filepath.Join(dir, "sing-box")
		if err := os.WriteFile(want, []byte("fake"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := findSingbox(dir); got != want {
			t.Errorf("findSingbox flat = %q, want %q", got, want)
		}
	})

	// <prefix>/bin/tenebra-core resolves sing-box from <prefix>/lib/tenebra,
	// <prefix>/libexec/tenebra and <prefix>/share/tenebra — the conventions
	// distributions split on for a package's private files.
	for _, private := range []string{"lib", "libexec", "share"} {
		t.Run(private+" sibling", func(t *testing.T) {
			emptyPATH(t)
			prefix := t.TempDir()
			binDir := filepath.Join(prefix, "bin")
			helperDir := filepath.Join(prefix, private, "tenebra")
			if err := os.MkdirAll(binDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(helperDir, 0o755); err != nil {
				t.Fatal(err)
			}
			want := filepath.Join(helperDir, "sing-box")
			if err := os.WriteFile(want, []byte("fake"), 0o644); err != nil {
				t.Fatal(err)
			}
			if got := findSingbox(binDir); got != want {
				t.Errorf("findSingbox %s = %q, want %q", private, got, want)
			}
		})
	}

	t.Run("beside the executable wins", func(t *testing.T) {
		// A binary next to the core is the one that was installed with it; a
		// copy in a shared helper directory must not shadow it.
		emptyPATH(t)
		prefix := t.TempDir()
		binDir := filepath.Join(prefix, "bin")
		helperDir := filepath.Join(prefix, "lib", "tenebra")
		if err := os.MkdirAll(binDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(helperDir, 0o755); err != nil {
			t.Fatal(err)
		}
		want := filepath.Join(binDir, "sing-box")
		for _, p := range []string{want, filepath.Join(helperDir, "sing-box")} {
			if err := os.WriteFile(p, []byte("fake"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if got := findSingbox(binDir); got != want {
			t.Errorf("findSingbox = %q, want the neighbour %q", got, want)
		}
	})

	t.Run("PATH is the last resort", func(t *testing.T) {
		// The distribution-dependency layout: sing-box is a package of its own in
		// a directory that knows nothing about Tenebra, reachable only on PATH.
		pathDir := t.TempDir()
		want := filepath.Join(pathDir, "sing-box")
		if err := os.WriteFile(want, []byte("fake"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", pathDir)

		if got := findSingbox(t.TempDir()); got != want {
			t.Errorf("findSingbox = %q, want the PATH hit %q", got, want)
		}
	})

	t.Run("an install beats PATH", func(t *testing.T) {
		// With both present the copy Tenebra installed wins, so a packaged
		// version pin is never silently displaced by whatever is on PATH.
		pathDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(pathDir, "sing-box"), []byte("fake"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", pathDir)

		exeDir := t.TempDir()
		want := filepath.Join(exeDir, "sing-box")
		if err := os.WriteFile(want, []byte("fake"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := findSingbox(exeDir); got != want {
			t.Errorf("findSingbox = %q, want the installed %q", got, want)
		}
	})
}

// TestRuleSetCandidatesLeadWithSingboxDir: the directory holding the configured
// sing-box stays the first place probed, so the self-contained layouts keep
// working exactly as they did before the search grew a chain.
func TestRuleSetCandidatesLeadWithSingboxDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TENEBRA_SINGBOX", filepath.Join(dir, "sing-box"))

	got := ruleSetCandidates()
	if len(got) == 0 || got[0] != dir {
		t.Fatalf("ruleSetCandidates() = %v, want %q first", got, dir)
	}
	if slices.Contains(got[1:], dir) {
		t.Errorf("ruleSetCandidates() repeats %q: %v", dir, got)
	}
}

// TestRuleSetCandidatesCoverInstallDirs: with sing-box supplied by the
// distribution (its own /usr/bin package, holding no .srs), the rule-sets must
// still be found in the directories Tenebra's own package installs into.
func TestRuleSetCandidatesCoverInstallDirs(t *testing.T) {
	t.Setenv("TENEBRA_SINGBOX", "/usr/bin/sing-box")

	got := ruleSetCandidates()
	if got[0] != "/usr/bin" {
		t.Fatalf("ruleSetCandidates() = %v, want the sing-box dir first", got)
	}
	for _, want := range []string{"/usr/lib/tenebra", "/usr/share/tenebra"} {
		if !slices.Contains(got, want) {
			t.Errorf("ruleSetCandidates() = %v, missing %q", got, want)
		}
	}
}

// TestRuleSetDirFindsBundleAwayFromSingbox: the whole point of the chain — a
// sing-box in one directory and the .srs in another resolves to the directory
// that actually holds the rule-sets, instead of falling back to a slow remote
// download at every connect.
func TestRuleSetDirFindsBundleAwayFromSingbox(t *testing.T) {
	// The install dirs derived from the test binary's own location are not
	// writable in general, so drive the case through the one candidate a test
	// controls: an override pointing somewhere with no .srs must not stop a
	// later candidate from answering. Here the later candidate is arranged by
	// pointing the override at a directory that does hold them, and asserting
	// the earlier, empty one is skipped.
	empty := t.TempDir()
	full := t.TempDir()
	for _, f := range ruleSetFiles {
		if err := os.WriteFile(filepath.Join(full, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	t.Setenv("TENEBRA_SINGBOX", filepath.Join(empty, "sing-box"))
	if got := ruleSetDir(); got != "" {
		t.Errorf("ruleSetDir with an empty sing-box dir = %q, want the remote fallback", got)
	}

	t.Setenv("TENEBRA_SINGBOX", filepath.Join(full, "sing-box"))
	if got := ruleSetDir(); got != full {
		t.Errorf("ruleSetDir = %q, want %q", got, full)
	}
}

// TestRootDataDirIsMachineScoped: the store the system service pins must be the
// FHS location for state a service owns. /run would be wrong (wiped on boot,
// taking every imported profile with it) and so would anything under a user's
// home, which a root daemon has no business writing credentials into.
func TestRootDataDirIsMachineScoped(t *testing.T) {
	const want = "/var/lib/tenebra/data"
	if rootDataDir != want {
		t.Errorf("rootDataDir = %q, want %q", rootDataDir, want)
	}
}
