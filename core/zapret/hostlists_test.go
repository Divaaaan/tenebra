package zapret

import (
	"os"
	"path/filepath"
	"testing"
)

// writeLists lays out a bundle's lists directory with the given file contents.
func writeLists(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	lists := filepath.Join(dir, "lists")
	if err := os.MkdirAll(lists, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(lists, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func has(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// The coverage answer decides which services are safe to leave on the direct
// path, so it has to come from the bundle's own lists rather than a guess.
func TestCoveredReadsTheBundleLists(t *testing.T) {
	dir := writeLists(t, map[string]string{
		"list-general.txt":      "discord.com\nDiscordApp.net\n\n# a comment\ndiscord.gg\n",
		"list-general-user.txt": "# Never leave this file empty\ndomain.example.abc\nmy-site.example\n",
		"list-google.txt":       "*.googlevideo.com\n.youtube.com\nytimg.com.\n",
	})

	got := Covered(dir)

	for _, want := range []string{
		"discord.com", "discordapp.net", "discord.gg",
		"googlevideo.com", "youtube.com", "ytimg.com", "my-site.example",
	} {
		if !has(got, want) {
			t.Errorf("coverage misses %q (got %v)", want, got)
		}
	}
	// The bundle ships this entry only to keep the file non-empty; reporting it as
	// covered would make the coverage describe a domain nothing acts on.
	if has(got, "domain.example.abc") {
		t.Error("the placeholder entry was reported as covered")
	}
	if has(got, "# a comment") {
		t.Error("a comment line was read as a domain")
	}
}

// No bundle, or an unreadable one, must report nothing rather than something:
// the caller treats empty as "coverage unknown" and keeps the speculative half
// of the preset in the tunnel. Returning a partial guess here would move those
// services onto a path nothing is bypassing.
func TestCoveredWithoutListsIsEmpty(t *testing.T) {
	if got := Covered(t.TempDir()); len(got) != 0 {
		t.Fatalf("coverage without lists = %v, want empty", got)
	}
}
