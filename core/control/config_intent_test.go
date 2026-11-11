package control

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/Divaaaan/tenebra/core/maintenance"
	"github.com/Divaaaan/tenebra/core/routing"
	"github.com/Divaaaan/tenebra/core/singbox"
	"github.com/Divaaaan/tenebra/core/state"
)

func TestConfigIntentIgnoresRuntimeBindings(t *testing.T) {
	ro := routing.Options{Mode: routing.ModeGlobal, SplitApps: []string{"game.exe"}}
	tun := singbox.TunOptions{}
	snapshot := state.Snapshot{Connection: state.ConnectionIntent{Selection: state.Selection{Kind: state.Auto}}}
	before := captureConfigIntent(ro, tun, snapshot.Connection, snapshot.Multihop)
	ro.RuleSetDir, ro.MultihopEntry, ro.MultihopExit = "/another/runtime", "probe-in3", "node-runtime-tag"
	tun.ClashAPIPort, tun.Address, tun.Address6, tun.CacheDir = 18888, "10.9.8.1/30", "fd99::1/126", "/run/private"
	tun.Maintenance = &maintenance.InboundConfig{RunID: "rotated", Username: "private-user", Password: "private-password"}
	if !before.Equal(captureConfigIntent(ro, tun, snapshot.Connection, snapshot.Multihop)) {
		t.Fatal("per-run bindings changed desired engine intent")
	}
	cases := map[string]func(*routing.Options, *singbox.TunOptions, *state.MultihopRequirement){
		"DNS": func(r *routing.Options, _ *singbox.TunOptions, _ *state.MultihopRequirement) {
			r.DNSDirect = "tls://9.9.9.9"
		},
		"IPv4Only": func(r *routing.Options, _ *singbox.TunOptions, _ *state.MultihopRequirement) { r.IPv4Only = true },
		"split": func(r *routing.Options, _ *singbox.TunOptions, _ *state.MultihopRequirement) {
			r.SplitMode = routing.SplitInclude
		},
		"mixed port":   func(_ *routing.Options, t *singbox.TunOptions, _ *state.MultihopRequirement) { t.MixedPort = 23456 },
		"MTU":          func(_ *routing.Options, t *singbox.TunOptions, _ *state.MultihopRequirement) { t.MTU = 1400 },
		"external TUN": func(_ *routing.Options, t *singbox.TunOptions, _ *state.MultihopRequirement) { t.ExternalTun = true },
		"chain":        func(_ *routing.Options, _ *singbox.TunOptions, m *state.MultihopRequirement) { m.Enabled = true },
		"covered bypass": func(r *routing.Options, _ *singbox.TunOptions, _ *state.MultihopRequirement) {
			r.ZapretCovered = []string{"youtube"}
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			r, engine, chain := routing.Options{Mode: routing.ModeGlobal, SplitApps: []string{"game.exe"}}, singbox.TunOptions{}, snapshot.Multihop
			change(&r, &engine, &chain)
			if before.Equal(captureConfigIntent(r, engine, snapshot.Connection, chain)) {
				t.Fatal("user intent was ignored")
			}
		})
	}
}

func TestConfigIntentEveryOptionHasAnExplicitClassification(t *testing.T) {
	for _, table := range []struct {
		typ    reflect.Type
		fields map[string]bool
	}{
		{reflect.TypeFor[routing.Options](), map[string]bool{
			"Mode": true, "BypassLAN": true, "IPv4Only": true, "KillSwitch": true, "TLSFragment": true, "DNSRemote": true, "DNSDirect": true,
			"AdBlock": true, "ZapretActive": true, "ZapretCovered": true, "UnblockServices": true, "GamesDirect": true, "VoiceDirect": true,
			"SplitMode": true, "SplitApps": true, "RulesDirect": true, "RulesProxy": true, "PresetRuBanking": true, "PresetRuGov": true,
			"Multihop": true, "MultihopEntry": false, "MultihopExit": false, "RuleSetDir": false}},
		{reflect.TypeFor[singbox.TunOptions](), map[string]bool{
			"Mode": true, "MixedPort": true, "InterfaceName": true, "MTU": true, "Stack": true, "ExternalTun": true,
			"ClashAPIPort": false, "Address": false, "Address6": false, "CacheDir": false, "Maintenance": false}},
	} {
		if table.typ.NumField() != len(table.fields) {
			t.Fatalf("classify every %s field explicitly", table.typ.Name())
		}
		for index := 0; index < table.typ.NumField(); index++ {
			if _, ok := table.fields[table.typ.Field(index).Name]; !ok {
				t.Fatalf("unclassified option %s", table.typ.Field(index).Name)
			}
		}
	}
}

func TestConnectionIntentIgnoresDisplayAndSchedulingSettings(t *testing.T) {
	_, store, _ := newCanonicalTestDaemon(t)
	snapshot := store.View()
	before, err := connectionIntent(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	yes := true
	snapshot.Preferences.CrashReports, snapshot.Preferences.AutoFailover = &yes, &yes
	snapshot.Connection.Autoconnect = !snapshot.Connection.Autoconnect
	after, err := connectionIntent(snapshot)
	if err != nil || !before.Equal(after) {
		t.Fatalf("non-engine settings changed intent: %v", err)
	}
	snapshot.Preferences.IPv4Only = !snapshot.Preferences.IPv4Only
	after, err = connectionIntent(snapshot)
	if err != nil || before.Equal(after) {
		t.Fatalf("effective DNS policy did not change intent: %v", err)
	}
}

func TestConnectionIntentTracksNodeConfigWithoutDisplayOrSecretExposure(t *testing.T) {
	_, store, _ := newCanonicalTestDaemon(t)
	p := addCanonicalProfile(t, store)
	snapshot := store.View()
	snapshot.Connection.ProfileID = p.ID
	before, err := connectionIntent(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	p.Name, p.Servers[0].Name, p.Revision = "renamed profile", "renamed node", p.Revision+1
	snapshot.Profiles[p.ID] = p
	after, err := connectionIntent(snapshot)
	if err != nil || !before.Equal(after) {
		t.Fatalf("display-only revision changed engine input: %v", err)
	}
	p.Servers[0].TLS.ServerName = "changed.test"
	snapshot.Profiles[p.ID] = p
	after, err = connectionIntent(snapshot)
	if err != nil || before.Equal(after) {
		t.Fatalf("changed node TLS input was ignored: %v", err)
	}
	printed := fmt.Sprintf("%v %#v", before, before)
	for _, private := range []string{"SYNTHETIC_ONLY", "node.test", "sni.test"} {
		if strings.Contains(printed, private) {
			t.Fatal("private engine material escaped via formatting")
		}
	}
}

func TestConfigIntentOnlyCapturesTheCurrentProfilesChain(t *testing.T) {
	connection := state.ConnectionIntent{ProfileID: "p1", Selection: state.Selection{Kind: state.Auto}}
	chain := state.MultihopRequirement{Enabled: true, Bindings: []state.Binding{
		{ProfileID: "p1", Entry: state.NodeRef{NodeID: "entry-1"}, Exit: state.NodeRef{NodeID: "exit-1"}},
		{ProfileID: "p2", Entry: state.NodeRef{NodeID: "entry-2"}, Exit: state.NodeRef{NodeID: "exit-2"}},
	}}
	before := captureConfigIntent(routing.Options{}, singbox.TunOptions{}, connection, chain)
	chain.Bindings[1].Entry.NodeID = "new-entry-2"
	if !before.Equal(captureConfigIntent(routing.Options{}, singbox.TunOptions{}, connection, chain)) {
		t.Fatal("dormant profile chain changed active engine intent")
	}
	chain.Bindings[0].Entry.NodeID = "new-entry-1"
	if before.Equal(captureConfigIntent(routing.Options{}, singbox.TunOptions{}, connection, chain)) {
		t.Fatal("active profile chain was ignored")
	}
}
