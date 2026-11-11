package control

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/Divaaaan/tenebra/core/model"
	"github.com/Divaaaan/tenebra/core/profile"
	"github.com/Divaaaan/tenebra/core/state"
	"github.com/Divaaaan/tenebra/core/subscription"
	"github.com/Divaaaan/tenebra/internal/securefs"
)

func assertCanonicalEmittedSNI(t *testing.T, d *Daemon, p profile.Profile, nodeID, wantSNI string) {
	t.Helper()
	runner := d.runner.(*fakeRunner)
	d.probeWarmup = time.Millisecond
	d.ifacePresent = func(string) bool { return true }
	defer d.handleDisconnect(Request{Cmd: CmdDisconnect})
	response := d.Handle(context.Background(), Request{Cmd: CmdConnectV2, Profile: p.ID, Selection: &SelectionView{Kind: "pinned", Ref: &NodeRefView{ProfileID: p.ID, NodeID: nodeID, Resolution: "resolved"}}})
	if !response.Ok {
		t.Fatal(response.Error)
	}
	var raw []byte
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		runner.mu.Lock()
		raw = append([]byte(nil), runner.lastCfg...)
		runner.mu.Unlock()
		if len(raw) > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if len(raw) == 0 {
		t.Fatal("typed selection did not emit a config")
	}
	var config struct {
		Outbounds []map[string]json.RawMessage `json:"outbounds"`
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	var selected string
	byTag := map[string]map[string]json.RawMessage{}
	for _, outbound := range config.Outbounds {
		var tag string
		json.Unmarshal(outbound["tag"], &tag)
		byTag[tag] = outbound
		if tag == "proxy" {
			json.Unmarshal(outbound["default"], &selected)
		}
	}
	var tls struct {
		ServerName string `json:"server_name"`
	}
	if json.Unmarshal(byTag[selected]["tls"], &tls) != nil || tls.ServerName != wantSNI {
		t.Fatalf("typed identity emitted wrong SNI: selected=%q got=%q want=%q", selected, tls.ServerName, wantSNI)
	}
}
func TestCanonicalSNISelectionSurvivesRefreshAndRestart(t *testing.T) {
	d, store, data := newCanonicalTestDaemon(t)
	node := func(sni string) model.Node {
		return model.Node{Protocol: model.Trojan, Name: "same display", Server: "same.test", Port: 443, Password: "SYNTHETIC_ONLY", TLS: &model.TLS{Enabled: true, ServerName: sni}}
	}
	p, err := profile.NewProfile("Friendly", profile.SourceSubscription, "https://provider.test/private", []model.Node{node("one.test"), node("two.test")})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Profiles().Add(p); err != nil {
		t.Fatal(err)
	}
	p, _ = store.Profiles().Get(p.ID)
	selected := p.Servers[1].ID
	assertCanonicalEmittedSNI(t, d, p, selected, "two.test")
	d.fetch = func(context.Context, string) ([]byte, http.Header, error) {
		return []byte("trojan://SYNTHETIC_ONLY@same.test:443?security=tls&sni=two.test#renamed\ntrojan://SYNTHETIC_ONLY@same.test:443?security=tls&sni=one.test#same"), http.Header{}, nil
	}
	// The first helper explicitly disconnected. Permit unprotected maintenance
	// in this identity/restart fixture; production's unknown mode stays deferred.
	d.subscriptionOps.accepted = subscriptionAcceptedInfo{ConfirmedUnprotected: true}
	d.subscriptionOps.fetch = func(ctx context.Context, url string, _ subscription.FetchOptions) ([]byte, http.Header, error) {
		return d.fetch(ctx, url)
	}
	t.Cleanup(d.subscriptionOps.close)
	p, _, err = d.refreshProfile(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if p.Servers[0].ID != selected {
		t.Fatal("SNI variant rebound after incoming reorder")
	}
	assertCanonicalEmittedSNI(t, d, p, selected, "two.test")
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	directory, err := data.Path()
	if err != nil {
		t.Fatal(err)
	}
	backup, err := securefs.OpenRoot(filepath.Join(filepath.Dir(directory), "backup"), securefs.Policy{Mode: securefs.SameAccountPrivate}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	reopened, err := state.Open(context.Background(), data, backup)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.Admit(); err != nil {
		t.Fatal(err)
	}
	second := newUnitTestDaemon(reopened.Profiles(), newFakeRunner())
	t.Cleanup(second.entCancel)
	if err := second.SetCanonicalState(reopened); err != nil {
		t.Fatal(err)
	}
	p, _ = reopened.Profiles().Get(p.ID)
	assertCanonicalEmittedSNI(t, second, p, selected, "two.test")
}
