package control

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/Divaaaan/tenebra/core/model"
	"github.com/Divaaaan/tenebra/core/profile"
	"github.com/Divaaaan/tenebra/core/protection"
	"github.com/Divaaaan/tenebra/core/state"
	"github.com/Divaaaan/tenebra/internal/securefs"
)

func newCanonicalTestDaemon(t *testing.T) (*Daemon, *state.Store, *securefs.Root) {
	t.Helper()
	if base := os.Getenv("TENEBRA_SECUREFS_TEST_ROOT"); base != "" {
		t.Setenv("GOTMPDIR", filepath.Clean(base))
	} else if runtime.GOOS == "windows" {
		t.Fatal("set explicit protected TENEBRA_SECUREFS_TEST_ROOT")
	}
	policy := securefs.Policy{Mode: securefs.SameAccountPrivate}
	parent, err := securefs.OpenRoot(filepath.Join(t.TempDir(), "private"), policy, true)
	if err != nil {
		t.Fatal(err)
	}
	data, err := parent.OpenDir("data", policy, true)
	if err != nil {
		t.Fatal(err)
	}
	backup, err := parent.OpenDir("backup", policy, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, closer := range []*securefs.Root{data, backup, parent} {
			if err := closer.Close(); err != nil {
				t.Error(err)
			}
		}
	})
	store, err := state.Open(context.Background(), data, backup)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := store.Admit(); err != nil {
		t.Fatal(err)
	}
	d := newUnitTestDaemon(store.Profiles(), newFakeRunner())
	t.Cleanup(func() { d.entCancel(); d.entWG.Wait() })
	if err := d.SetCanonicalState(store); err != nil {
		t.Fatal(err)
	}
	return d, store, data
}

func TestCanonicalDNSRetainsEncryptedResolverWhileGuardStillEnforced(t *testing.T) {
	d, store, _ := newCanonicalTestDaemon(t)
	d.SetProtection(protection.New(&fakeHostProtection{}))
	if err := d.protection.Prepare(); err != nil {
		t.Fatal(err)
	}
	if store.View().Preferences.KillSwitch {
		t.Fatal("fixture must have desired OFF with still-enforced host guard")
	}
	before := store.View()
	response := d.Handle(context.Background(), Request{Cmd: CmdSetDNS, DNSDirect: "8.8.8.8", DNSRemote: "https://remote.test/dns-query"})
	if response.Ok || response.Error != "invalid_request" || !reflect.DeepEqual(before, store.View()) {
		t.Fatal("DNS change bypassed still-enforced guard", response)
	}
}

type canonicalSettingsSpy struct{ loads, saves int }

func (s *canonicalSettingsSpy) Load() persistedSettings {
	s.loads++
	return persistedSettings{KillSwitch: true}
}
func (s *canonicalSettingsSpy) Save(persistedSettings) error { s.saves++; return nil }

type canonicalLastGoodSpy struct{ gets, sets int }

func (s *canonicalLastGoodSpy) Get(string) (string, bool) { s.gets++; return "OLD_HASH", true }
func (s *canonicalLastGoodSpy) Set(string, string)        { s.sets++ }

// A second link makes only this fixture's canonical leaf untrusted. No ACL,
// host policy or real disk pressure is changed by the storage fault.
func blockCanonicalFile(t *testing.T, data *securefs.Root) func() {
	t.Helper()
	directory, err := data.Path()
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "canonical-test-link")
	if err := os.Link(filepath.Join(directory, "profiles.json"), link); err != nil {
		t.Fatal(err)
	}
	unblocked := false
	clear := func() {
		if !unblocked {
			if err := os.Remove(link); err != nil {
				t.Error(err)
			}
			unblocked = true
		}
	}
	t.Cleanup(clear)
	return clear
}
func TestCanonicalInstallDisablesAllLegacyWriters(t *testing.T) {
	d, store, _ := newCanonicalTestDaemon(t)
	settings, hints := &canonicalSettingsSpy{}, &canonicalLastGoodSpy{}
	d.SetSettings(settings)
	d.SetLastGood(hints)
	d.persistSettings()
	d.lastGood.Get("unknown")
	d.lastGood.Set("unknown", "old-hash")
	if settings.loads != 0 || settings.saves != 0 || hints.gets != 0 || hints.sets != 0 {
		t.Fatal("canonical installation reactivated a legacy reader/writer")
	}
	if err := d.SetCanonicalState(store); err == nil {
		t.Fatal("second canonical repository installation accepted")
	}
}
func TestCanonicalPreferenceFailureDoesNotProject(t *testing.T) {
	d, store, data := newCanonicalTestDaemon(t)
	before := store.View()
	desired := d.snapshotState()
	unblock := blockCanonicalFile(t, data)
	defer unblock()
	response := d.Handle(context.Background(), Request{ID: 1, Cmd: CmdSetSplit, Mode: "include", Apps: []string{"literal[app].exe"}})
	if response.Ok || response.Error != "persistence_error" {
		t.Fatal("failed storage reported as saved", response)
	}
	if !reflect.DeepEqual(store.View(), before) || !reflect.DeepEqual(d.snapshotState(), desired) {
		t.Fatal("failed persistence projected desired changes")
	}
}
func TestCanonicalAllPreferencePathsUseOneCommitGate(t *testing.T) {
	on, off := true, false
	for _, req := range []Request{
		{Cmd: CmdSetRouting, Mode: "global"}, {Cmd: CmdSetSplit, Mode: "exclude", Apps: []string{"one.exe"}},
		{Cmd: CmdSetPresets, Games: &on, Voice: &off}, {Cmd: CmdSetKillSwitch, On: true}, {Cmd: CmdSetTLSFragment, On: true},
		{Cmd: CmdSetProxyMode, ProxyMode: "system-proxy", ProxyPort: 18181}, {Cmd: CmdSetTun, Stack: "gvisor"},
		{Cmd: CmdSetAutoconnect, On: true}, {Cmd: CmdSetAutoFailover, On: false}, {Cmd: CmdSetCrashReports, On: true},
		{Cmd: CmdSetDNS, AdBlock: true, IPv4Only: true, DNSRemote: "https://dns.test/dns-query", DNSDirect: "https://direct.test/dns-query"},
		{Cmd: CmdSetRules, RulesDirect: []string{"Direct.test"}, RulesProxy: []string{"proxy.test"}, PresetRuBanking: true},
		{Cmd: CmdSetZapretAutoUpdate, On: false},
	} {
		t.Run(req.Cmd, func(t *testing.T) {
			d, store, data := newCanonicalTestDaemon(t)
			before := store.View()
			desired := d.snapshotState()
			unblock := blockCanonicalFile(t, data)
			response := d.Handle(context.Background(), req)
			if response.Ok || response.Error != "persistence_error" || !reflect.DeepEqual(store.View(), before) || !reflect.DeepEqual(d.snapshotState(), desired) {
				t.Fatal("preference bypassed durable commit gate", response)
			}
			unblock()
			response = d.Handle(context.Background(), req)
			if !response.Ok {
				t.Fatal(response.Error)
			}
			after := store.View()
			if after.Revision != before.Revision+1 {
				t.Fatal("preference not committed exactly once")
			}
			if !reflect.DeepEqual(after.Profiles, before.Profiles) || !reflect.DeepEqual(after.Multihop, before.Multihop) || !reflect.DeepEqual(after.LastGood, before.LastGood) {
				t.Fatal("preference rewrote unrelated identity data")
			}
			if again := d.Handle(context.Background(), req); !again.Ok || store.View().Revision != after.Revision {
				t.Fatal("idempotent preference rewrote storage", again)
			}
		})
	}
}
func TestCanonicalProjectionRejectsDelayedOlderCommit(t *testing.T) {
	d, store, _ := newCanonicalTestDaemon(t)
	newer := store.View()
	newer.Revision = 9
	newer.Preferences.TLSFragment = true
	if _, ok := d.projectRepository(newer); !ok {
		t.Fatal("new projection refused")
	}
	older := store.View()
	older.Revision = 8
	if _, ok := d.projectRepository(older); ok {
		t.Fatal("older projection accepted")
	}
	if _, ok := d.projectRepository(newer); ok {
		t.Fatal("equal projection accepted")
	}
	if !d.snapshotState().TLSFragment {
		t.Fatal("delayed projection reverted desired preference")
	}
	view := d.snapshotState()
	view.Selection.Kind = "mutated"
	if d.snapshotState().Selection.Kind == "mutated" {
		t.Fatal("wire selection aliases desired state")
	}
}
func addCanonicalProfile(t *testing.T, store *state.Store) profile.Profile {
	t.Helper()
	p, err := profile.NewProfile("Friendly", profile.SourceSubscription, "https://provider.test/private", []model.Node{{Protocol: model.Trojan, Name: "node", Server: "node.test", Port: 443, Password: "SYNTHETIC_ONLY", TLS: &model.TLS{Enabled: true, ServerName: "sni.test"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Profiles().Add(p); err != nil {
		t.Fatal(err)
	}
	p, _ = store.Profiles().Get(p.ID)
	return p
}
func TestIdentityLegacyMutationNeedsReselect(t *testing.T) {
	d, store, _ := newCanonicalTestDaemon(t)
	p := addCanonicalProfile(t, store)
	for _, cmd := range []string{CmdConnect, CmdSetMultihop} {
		response := d.Handle(context.Background(), Request{Cmd: cmd, Profile: p.ID, Node: "0123456789abcdef", EntryID: "0123456789abcdef", Enabled: true})
		encoded, _ := json.Marshal(response)
		if response.Ok || response.Error != "reselect_required" || bytesContains(encoded, []byte("0123456789abcdef")) {
			t.Fatal("legacy hash mutation escaped capability fence", response)
		}
	}
	if response := d.Handle(context.Background(), Request{Cmd: CmdStatus}); !response.Ok {
		t.Fatal(response)
	}
	if response := d.Handle(context.Background(), Request{Cmd: CmdListProfiles}); !response.Ok {
		t.Fatal(response)
	}
	if response := d.Handle(context.Background(), Request{Cmd: CmdConnectV2, Profile: p.ID, Selection: &SelectionView{Kind: "pinned", Ref: &NodeRefView{ProfileID: p.ID, NodeID: "n2_00000000000000000000000000000000", Resolution: "resolved"}}}); response.Ok {
		t.Fatal("caller resolution was treated as authority")
	}
}
func bytesContains(value, needle []byte) bool { return strings.Contains(string(value), string(needle)) }
func TestCanonicalLastGoodFailureDoesNotBreakAcceptedVPN(t *testing.T) {
	d, store, data := newCanonicalTestDaemon(t)
	p := addCanonicalProfile(t, store)
	d.mu.Lock()
	d.state.State, d.state.Profile, d.state.Node = StateConnected, p.ID, p.Servers[0].ID
	d.mu.Unlock()
	unblock := blockCanonicalFile(t, data)
	defer unblock()
	d.lastGood.Set(p.ID, p.Servers[0].ID)
	if d.snapshotState().State != StateConnected || len(store.View().LastGood) != 0 {
		t.Fatal("cache write failure dropped accepted connection or published unsaved hint")
	}
	if err := store.Fence(context.Background()); err != nil && !errors.Is(err, state.ErrDurabilityUnknown) {
		t.Fatal(err)
	}
}
