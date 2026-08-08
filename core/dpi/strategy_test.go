package dpi

import (
	"strings"
	"testing"
)

// TestDefaultStrategiesLeadWithTheVerifiedPreset pins the head of the cascade to
// the option set that was checked end to end against a live network. A reorder
// that demotes it should be a deliberate edit, not a merge accident.
func TestDefaultStrategiesLeadWithTheVerifiedPreset(t *testing.T) {
	if len(DefaultStrategies) == 0 {
		t.Fatal("DefaultStrategies is empty")
	}
	first := DefaultStrategies[0]
	want := []string{"--split", "1", "--disorder", "3+s", "--mod-http=h,d", "--auto=torst", "--tlsrec", "1+s"}
	if !equalArgs(first.Args, want) {
		t.Errorf("first strategy args = %q, want the verified preset %q", first.Args, want)
	}
}

// TestDefaultStrategiesValidate is the important one: the presets go through the
// same allowlist as anything a user types, so a typo in a shipped preset fails
// here rather than at spawn time on a user's machine.
func TestDefaultStrategiesValidate(t *testing.T) {
	for _, s := range DefaultStrategies {
		if err := s.Validate(); err != nil {
			t.Errorf("strategy %q: %v", s.Name, err)
		}
	}
}

func TestDefaultStrategiesHaveUniqueNames(t *testing.T) {
	seen := make(map[string]bool, len(DefaultStrategies))
	for _, s := range DefaultStrategies {
		if s.Name == "" {
			t.Error("strategy with an empty name")
			continue
		}
		if seen[s.Name] {
			t.Errorf("duplicate strategy name %q", s.Name)
		}
		seen[s.Name] = true
	}
}

// TestDefaultStrategiesBuildArgs proves each preset renders a complete command
// line, which is the only form the runner ever sees.
func TestDefaultStrategiesBuildArgs(t *testing.T) {
	for _, s := range DefaultStrategies {
		argv, err := BuildArgs(LoopbackHost, DefaultPort, s.Args)
		if err != nil {
			t.Fatalf("BuildArgs for %q: %v", s.Name, err)
		}
		if len(argv) < 4 || argv[0] != "-i" || argv[2] != "-p" {
			t.Errorf("argv for %q = %q, want the listener first", s.Name, argv)
		}
	}
}

func TestStrategyValidateRejectsBadInput(t *testing.T) {
	if err := (Strategy{Args: []string{"--split", "1"}}).Validate(); err == nil {
		t.Error("a nameless strategy validated")
	}
	err := Strategy{Name: "evil", Args: []string{"--cache-dump", "out.txt"}}.Validate()
	if err == nil {
		t.Fatal("a strategy writing files validated")
	}
	if !strings.Contains(err.Error(), "evil") {
		t.Errorf("error %q does not name the offending strategy", err)
	}
}

func TestLookup(t *testing.T) {
	want := DefaultStrategies[0]
	got, ok := Lookup(want.Name)
	if !ok {
		t.Fatalf("Lookup(%q) not found", want.Name)
	}
	if got.Name != want.Name || !equalArgs(got.Args, want.Args) {
		t.Errorf("Lookup(%q) = %+v, want %+v", want.Name, got, want)
	}

	if _, ok := Lookup("no-such-strategy"); ok {
		t.Error("Lookup of an unknown name reported found")
	}
	if _, ok := Lookup(""); ok {
		t.Error("Lookup of an empty name reported found")
	}
}

// TestLookupDoesNotShareArgs guards the shared package variable: a caller that
// edits the returned args must not rewrite the preset for everyone else.
func TestLookupDoesNotShareArgs(t *testing.T) {
	got, ok := Lookup(DefaultStrategies[0].Name)
	if !ok {
		t.Fatal("preset missing")
	}
	if len(got.Args) == 0 {
		t.Fatal("preset has no args")
	}
	got.Args[0] = "tampered"
	if DefaultStrategies[0].Args[0] == "tampered" {
		t.Error("Lookup returned the package variable's backing array")
	}
}
