package state

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"testing"

	"github.com/Divaaaan/tenebra/core/model"
	"github.com/Divaaaan/tenebra/core/nodeidentity"
	"github.com/Divaaaan/tenebra/core/profile"
)

func nextTestID() func() (string, error) {
	counter := 0
	return func() (string, error) { counter++; return fmt.Sprintf("n2_%032x", counter), nil }
}

func TestCanonicalLegacyMalformedUnicodeRefusesWithoutReplacement(t *testing.T) {
	for _, settings := range []string{`{"version":2,"split_apps":["\ud800"]}`, `{"version":2,"future":{"\udfff":true}}`} {
		bundle := bundleFor(t, []profile.Profile{}, settings, "")
		before := cloneLegacyFiles(bundle)
		if _, err := MigrateLegacy(bundle, nextTestID()); err == nil {
			t.Fatal("lossy legacy Unicode was normalized instead of refused")
		}
		if !equalJSON(before, bundle) {
			t.Fatal("refusal changed raw legacy bytes")
		}
	}
}
func legacyNode(id, sni string) profile.Server {
	return profile.Server{ID: id, Node: model.Node{Protocol: model.Trojan, Name: "display", Server: "example.test", Port: 443, Password: "SYNTHETIC_ONLY", TLS: &model.TLS{Enabled: true, ServerName: sni}}}
}
func bundleFor(t *testing.T, profiles []profile.Profile, settings, hints string) LegacyBundle {
	t.Helper()
	bundle := LegacyBundle{}
	if profiles != nil {
		data, err := json.Marshal(profiles)
		if err != nil {
			t.Fatal(err)
		}
		bundle.Profiles = LegacyFile{true, data}
	}
	if settings != "" {
		bundle.Settings = LegacyFile{true, []byte(settings)}
	}
	if hints != "" {
		bundle.LastGood = LegacyFile{true, []byte(hints)}
	}
	return bundle
}

func TestCanonicalMigrationPreservesEveryLegacyRow(t *testing.T) {
	rows := make([]profile.Server, 2049)
	for i := range rows {
		rows[i] = legacyNode("arbitrary_stored_id", "same.test")
	}
	bundle := bundleFor(t, []profile.Profile{{ID: "profile-kept", Name: "Friendly", Source: profile.SourceManual, Servers: rows}}, `{"version":2,"autoconnect":true,"last_profile":"profile-kept","last_node":"arbitrary_stored_id"}`, `{"profile-kept":"arbitrary_stored_id"}`)
	before := bytes.Clone(bundle.Profiles.Bytes)
	snapshot, err := MigrateLegacy(bundle, nextTestID())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Profiles) != 1 || len(snapshot.Profiles["profile-kept"].Servers) != 2049 {
		t.Fatal("migration trimmed or deduped legacy rows")
	}
	p := snapshot.Profiles["profile-kept"]
	seen := map[string]bool{}
	for _, server := range p.Servers {
		if !nodeidentity.ValidID(server.ID) || seen[server.ID] {
			t.Fatal("migration assigned invalid/duplicate opaque IDs")
		}
		seen[server.ID] = true
	}
	if snapshot.Connection.Selection.Kind != Pinned || snapshot.Connection.Selection.Ref.NodeID != p.Servers[0].ID || snapshot.LastGood[p.ID].NodeID != p.Servers[0].ID {
		t.Fatal("stored ID reference was recomputed or same-spec duplicate chose wrong row")
	}
	var budget *model.BudgetError
	if !errors.As(profile.ValidateForUse(p), &budget) || budget.Kind != "retained_nodes" {
		t.Fatal("stored over-budget legacy profile became operational")
	}
	if !bytes.Equal(before, bundle.Profiles.Bytes) {
		t.Fatal("pure migration mutated source bytes")
	}
	if err := snapshot.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestCanonicalMigrationReferencesByStoredID(t *testing.T) {
	for _, test := range []struct {
		name     string
		rows     []profile.Server
		pin      string
		resolved bool
	}{
		{"arbitrary", []profile.Server{legacyNode("not-a-hash", "a.test")}, "not-a-hash", true},
		{"clones", []profile.Server{legacyNode("shared", "a.test"), legacyNode("shared", "a.test")}, "shared", true},
		{"sni_collision", []profile.Server{legacyNode("shared", "a.test"), legacyNode("shared", "b.test")}, "shared", false},
		{"dangling", []profile.Server{legacyNode("exists", "a.test")}, "never-present", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			settings, _ := json.Marshal(map[string]any{"version": 2, "last_profile": "p", "last_node": test.pin})
			snapshot, err := MigrateLegacy(bundleFor(t, []profile.Profile{{ID: "p", Name: "n", Servers: test.rows}}, string(settings), ""), nextTestID())
			if err != nil {
				t.Fatal(err)
			}
			selection := snapshot.Connection.Selection
			if selection.Kind != Pinned {
				t.Fatal("nonempty pin became AUTO")
			}
			if test.resolved {
				if selection.Ref.Resolution != Resolved || selection.Ref.NodeID != snapshot.Profiles["p"].Servers[0].ID {
					t.Fatal("unique/equivalent stored ref not resolved")
				}
			} else {
				if selection.Ref.Resolution != NeedsSelection || selection.Ref.NodeID != "" || selection.Ref.IssueID == "" {
					t.Fatal("ambiguous/dangling pin was silently reset or chosen")
				}
			}
			if err := snapshot.Validate(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCanonicalMigrationMultihopAllCompatibleProfiles(t *testing.T) {
	profiles := []profile.Profile{
		{ID: "p1", Name: "one", Servers: []profile.Server{legacyNode("entry", "a.test"), legacyNode("exit", "b.test")}},
		{ID: "p2", Name: "two", Servers: []profile.Server{legacyNode("entry", "c.test"), legacyNode("exit", "d.test")}},
		{ID: "p3", Name: "three", Servers: []profile.Server{legacyNode("entry", "e.test")}},
	}
	snapshot, err := MigrateLegacy(bundleFor(t, profiles, `{"version":2,"multihop":true,"multihop_entry_id":"entry","multihop_exit_id":"exit"}`, ""), nextTestID())
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Multihop.Enabled {
		t.Fatal("unresolved chain was disabled")
	}
	for _, id := range []string{"p1", "p2"} {
		binding, err := snapshot.BindingFor(id)
		if err != nil || binding.Entry.ProfileID != id || binding.Exit.ProfileID != id {
			t.Fatalf("compatible profile %s lost binding: %v", id, err)
		}
	}
	if _, err := snapshot.BindingFor("p3"); !errors.Is(err, ErrNeedsSelection) {
		t.Fatal("incompatible profile degraded to single-hop")
	}
	if err := snapshot.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestCanonicalPreferencesV1V2StrictRoundTrip(t *testing.T) {
	for _, version := range []int{1, 2} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			settings := fmt.Sprintf(`{"version":%d,"preset_games_direct":true,"preset_voice_direct":true,"preset_unblock_services":false,"kill_switch":true,"crash_reports":false,"future_nested":{"a":[1,"x"]}}`, version)
			snapshot, err := MigrateLegacy(bundleFor(t, nil, settings, ""), nextTestID())
			if err != nil {
				t.Fatal(err)
			}
			preferences := snapshot.Preferences
			if preferences.Version != 3 || preferences.PresetGamesDirect == nil || *preferences.PresetGamesDirect != (version == 2) || preferences.PresetVoiceDirect == nil || *preferences.PresetVoiceDirect != (version == 2) {
				t.Fatal("legacy preset correction changed its version semantics")
			}
			if !preferences.KillSwitch || preferences.PresetUnblockServices == nil || *preferences.PresetUnblockServices || preferences.CrashReports == nil || *preferences.CrashReports || preferences.AutoFailover != nil {
				t.Fatal("unrelated/unset preferences lost")
			}
			encoded, err := json.Marshal(preferences)
			if err != nil {
				t.Fatal(err)
			}
			var decoded Preferences
			if err := json.Unmarshal(encoded, &decoded); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(preferences, decoded) || !bytes.Contains(encoded, []byte("future_nested")) {
				t.Fatal("future JSON/pointer state did not roundtrip")
			}
		})
	}
	for _, invalid := range []string{`{"version":99}`, `{"version":0}`, `null`, `{"version":2,"kill_switch":"bad"}`, `{"version":2,"version":1}`, `{broken`} {
		if _, err := MigrateLegacy(bundleFor(t, nil, invalid, ""), nextTestID()); err == nil {
			t.Fatal("corrupt/unknown preferences became defaults")
		}
	}
}

func TestCanonicalMalformedNullAndKnownCaseAliasesNeverBecomeDefaults(t *testing.T) {
	for _, settings := range []string{`{"version":2,"kill_switch":null}`, `{"version":2,"last_node":null}`, `{"version":2,"autoconnect":null}`, `{"version":2,"kill_switch":true,"Kill_Switch":false}`} {
		if _, err := MigrateLegacy(bundleFor(t, nil, settings, ""), nextTestID()); err == nil {
			t.Fatal("malformed known value became a default/ambiguous choice")
		}
	}
}

func TestCanonicalMigrationHintCorruptionDoesNotEraseIntent(t *testing.T) {
	snapshot, err := MigrateLegacy(bundleFor(t, []profile.Profile{{ID: "p", Name: "n", Servers: []profile.Server{legacyNode("chosen", "a.test")}}}, `{"version":2,"kill_switch":true,"last_profile":"p","last_node":"chosen"}`, `{broken`), nextTestID())
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Preferences.KillSwitch || snapshot.Connection.Selection.Kind != Pinned || snapshot.Connection.Selection.Ref.Resolution != Resolved {
		t.Fatal("corrupt hint erased protection/explicit intent")
	}
	found := false
	for _, issue := range snapshot.Issues {
		found = found || issue.Reason == "corrupt_hint_cache"
	}
	if !found {
		t.Fatal("hint corruption was silently ignored")
	}
}

func TestCanonicalMigrationSanitizesAliasesWithoutChangingURL(t *testing.T) {
	raw := "https://u:p@provider.test/private/PATH_CANARY?key=QUERY_CANARY"
	profiles := []profile.Profile{{ID: "p1", Name: raw, URL: raw}, {ID: "p2", Name: "Friendly", URL: raw}}
	snapshot, err := MigrateLegacy(bundleFor(t, profiles, `{"version":2}`, ""), nextTestID())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Profiles["p1"].Name != "provider.test" || snapshot.Profiles["p2"].Name != "Friendly" || snapshot.Profiles["p1"].URL != raw {
		t.Fatal("alias sanitizer changed identity/internal URL or ordinary label")
	}
}

func TestCanonicalMigrationAllocationFailureIsAtomic(t *testing.T) {
	bundle := bundleFor(t, []profile.Profile{{ID: "p", Name: "n", Servers: []profile.Server{legacyNode("old", "a.test")}}}, `{"version":2}`, "")
	before := bytes.Clone(bundle.Profiles.Bytes)
	if _, err := MigrateLegacy(bundle, func() (string, error) { return "", io.ErrUnexpectedEOF }); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatal("failed RNG migration was accepted")
	}
	if !bytes.Equal(before, bundle.Profiles.Bytes) {
		t.Fatal("failed candidate changed source bytes")
	}
}
