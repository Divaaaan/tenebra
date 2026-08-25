package control

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The bypass strategy used to be remembered in one global setting, so a laptop
// carried from home to a cafe came up on the strategy that won at home — on a
// network where it may do nothing at all, with routing already sending YouTube
// and Discord down the direct path because a bypass was "running". These cover
// the memory that is keyed by network instead, and the token that keys it.

// homeAndCafe drives one daemon between two networks. The fingerprint is a field
// precisely so a test can move the machine without a second network to plug into.
func homeAndCafe(t *testing.T) (*Daemon, *string) {
	t.Helper()
	d, _ := newTestDaemon(t)
	network := "home"
	d.netFingerprint = func() string { return fingerprintOf(network) }
	return d, &network
}

// TestTwoNetworksDoNotShareAStrategy is the bug itself: what was measured at
// home must not be handed to the cafe, and measuring at the cafe must not
// overwrite what is known about home.
func TestTwoNetworksDoNotShareAStrategy(t *testing.T) {
	d, network := homeAndCafe(t)

	d.rememberStrategyForThisNetwork("general (МГТС)")
	if got, ok := d.strategyForThisNetwork(); !ok || got != "general (МГТС)" {
		t.Fatalf("home remembered %q,%v right after measuring it", got, ok)
	}

	*network = "cafe"
	if got, ok := d.strategyForThisNetwork(); ok {
		t.Errorf("the cafe was handed %q, which was measured at home", got)
	}

	// A pick at the cafe finds a different answer, as it should — different DPI.
	d.rememberStrategyForThisNetwork("general (ALT2)")
	if got, ok := d.strategyForThisNetwork(); !ok || got != "general (ALT2)" {
		t.Errorf("the cafe remembered %q,%v, want its own answer", got, ok)
	}

	*network = "home"
	if got, ok := d.strategyForThisNetwork(); !ok || got != "general (МГТС)" {
		t.Errorf("back home the memory reads %q,%v; the cafe's answer overwrote it", got, ok)
	}
}

// TestUnrecognisedNetworkHasNoMemory: a machine whose network cannot be
// identified — every platform but Windows, and a Windows machine with no default
// gateway to read — must fall through to the single stored strategy rather than
// filing everything under one blank key, which is the global behaviour this
// replaced.
func TestUnrecognisedNetworkHasNoMemory(t *testing.T) {
	d, _ := newTestDaemon(t)
	d.netFingerprint = func() string { return "" }

	d.rememberStrategyForThisNetwork("general (ALT2)")
	if got, ok := d.strategyForThisNetwork(); ok {
		t.Errorf("an unidentifiable network reported a remembered %q", got)
	}
}

// TestRememberingIgnoresANonAnswer: only a measured winner is filed. An empty
// name would otherwise poison the entry for a network that had a good one.
func TestRememberingIgnoresANonAnswer(t *testing.T) {
	d, _ := homeAndCafe(t)

	d.rememberStrategyForThisNetwork("general (FAKE TLS AUTO)")
	d.rememberStrategyForThisNetwork("")
	if got, ok := d.strategyForThisNetwork(); !ok || got != "general (FAKE TLS AUTO)" {
		t.Errorf("memory reads %q,%v after an empty write", got, ok)
	}
}

// TestLeadWith covers the reordering that turns a repeat pick on a known network
// into one measurement: the remembered strategy is measured first, and because
// the run stops at the first strategy that carries everything, it is usually the
// only one measured.
func TestLeadWith(t *testing.T) {
	bundle := pickStrategies("general", "general (ALT2)", "general (МГТС)", "general (FAKE TLS AUTO)")

	cases := []struct {
		name  string
		first string
		want  []string
	}{
		{
			name:  "moves the remembered one to the front, rest in bundle order",
			first: "general (МГТС)",
			want:  []string{"general (МГТС)", "general", "general (ALT2)", "general (FAKE TLS AUTO)"},
		},
		{
			name:  "already first changes nothing",
			first: "general",
			want:  []string{"general", "general (ALT2)", "general (МГТС)", "general (FAKE TLS AUTO)"},
		},
		{
			// An update can retire a strategy. A stale cache entry must not cost the
			// run a candidate, or reorder it into something surprising.
			name:  "a name the bundle no longer has changes nothing",
			first: "general (RETIRED)",
			want:  []string{"general", "general (ALT2)", "general (МГТС)", "general (FAKE TLS AUTO)"},
		},
		{
			name:  "no memory changes nothing",
			first: "",
			want:  []string{"general", "general (ALT2)", "general (МГТС)", "general (FAKE TLS AUTO)"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := leadWith(bundle, c.first)
			if len(got) != len(c.want) {
				t.Fatalf("run has %d strategies, want %d: the reordering dropped one", len(got), len(c.want))
			}
			for i, want := range c.want {
				if got[i].Name != want {
					t.Errorf("position %d is %q, want %q", i, got[i].Name, want)
				}
			}
			// The reordering must not disturb the caller's slice: the bundle listing
			// is read again by everything else on the pick path.
			if bundle[0].Name != "general" {
				t.Error("leadWith rewrote the bundle it was handed")
			}
		})
	}
}

// TestLeadWithASingleStrategy: nothing to reorder, and nothing to trip over.
func TestLeadWithASingleStrategy(t *testing.T) {
	one := pickStrategies("general")
	if got := leadWith(one, "general"); len(got) != 1 || got[0].Name != "general" {
		t.Errorf("leadWith over a one-strategy bundle returned %v", got)
	}
	if got := leadWith(nil, "general"); got != nil {
		t.Errorf("leadWith over an empty bundle returned %v", got)
	}
}

// TestFingerprintIdentifiesWithoutDescribing is the privacy property. The token
// is a map key in a plain-text cache file — the kind of file a user pastes into
// an issue — so what it is derived from must not be readable out of it, and two
// networks must not collide into one entry.
func TestFingerprintIdentifiesWithoutDescribing(t *testing.T) {
	const home = "50:ff:20:0d:d2:e4@192.168.1.1"
	const cafe = "3c:37:86:aa:bb:cc@192.168.1.1" // same address, different router

	got := fingerprintOf(home)
	if got == "" {
		t.Fatal("a real identity fingerprinted to nothing")
	}
	if got != fingerprintOf(home) {
		t.Error("the same network fingerprinted differently twice; every launch would be a cache miss")
	}
	if got == fingerprintOf(cafe) {
		t.Error("two routers sharing a gateway address collided; one network would be handed the other's strategy")
	}
	for _, part := range []string{"50:ff:20:0d:d2:e4", "192.168.1.1", "50ff200dd2e4"} {
		if strings.Contains(got, part) {
			t.Errorf("the token %q carries %q; the cache file would say which network this is", got, part)
		}
	}
	if len(got) != 16 {
		t.Errorf("token is %d characters, want 16 hex", len(got))
	}
	if strings.Trim(got, "0123456789abcdef") != "" {
		t.Errorf("token %q is not hex; it is not a digest", got)
	}
}

// TestFingerprintOfNothingIsNothing: an unidentifiable network must stay
// unidentifiable rather than becoming the hash of the empty string, which every
// such machine would then share as one entry.
func TestFingerprintOfNothingIsNothing(t *testing.T) {
	if got := fingerprintOf(""); got != "" {
		t.Errorf("fingerprintOf(\"\") = %q, want empty", got)
	}
}

// TestFileNetStrategiesRoundTrip: what one store records is there for a store
// opened fresh over the same directory — the persistence that makes coming home
// cost one strategy instead of the bundle.
func TestFileNetStrategiesRoundTrip(t *testing.T) {
	dir := t.TempDir()

	s, err := OpenFileNetStrategies(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	s.Set("net-home", "general (МГТС)")
	s.Set("net-cafe", "general (ALT2)")
	s.Set("net-home", "general (FAKE TLS AUTO)") // the censor moved; re-measured

	s2, err := OpenFileNetStrategies(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got, ok := s2.Get("net-home"); !ok || got != "general (FAKE TLS AUTO)" {
		t.Errorf("net-home = %q,%v, want the re-measured answer", got, ok)
	}
	if got, ok := s2.Get("net-cafe"); !ok || got != "general (ALT2)" {
		t.Errorf("net-cafe = %q,%v, want general (ALT2)", got, ok)
	}
	if _, ok := s2.Get("net-never-seen"); ok {
		t.Error("a network never measured reported a strategy")
	}
}

// TestFileNetStrategiesIsItsOwnFile: the cache does not live in settings.json.
// That file holds choices the user made; this holds a measurement the app took,
// and clearing one must not clear the other.
func TestFileNetStrategiesIsItsOwnFile(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenFileNetStrategies(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	s.Set("net-home", "general (МГТС)")

	if _, err := os.Stat(filepath.Join(dir, netStrategyFile)); err != nil {
		t.Errorf("no %s written: %v", netStrategyFile, err)
	}
	if _, err := os.Stat(filepath.Join(dir, settingsFile)); !os.IsNotExist(err) {
		t.Errorf("the cache wrote into %s", settingsFile)
	}
}

// TestFileNetStrategiesCorruptFileIsEmpty: a mangled cache must cost a pick, not
// a daemon. It opens empty and relearns.
func TestFileNetStrategiesCorruptFileIsEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, netStrategyFile), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := OpenFileNetStrategies(dir)
	if err != nil {
		t.Fatalf("open over corrupt file: %v", err)
	}
	if _, ok := s.Get("net-home"); ok {
		t.Error("a corrupt cache file yielded an entry")
	}
	s.Set("net-home", "general")
	if got, ok := s.Get("net-home"); !ok || got != "general" {
		t.Errorf("the store did not relearn after a corrupt file: %q,%v", got, ok)
	}
}
