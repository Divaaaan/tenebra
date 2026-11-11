package control

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Divaaaan/tenebra/core/model"
	"github.com/Divaaaan/tenebra/core/profile"
)

func TestGenericURLAliasNeverLeavesDiagnostics(t *testing.T) {
	d, runner := newTestDaemon(t)
	const sourceURL = "https://alias.test/PRIVATE_ALIAS_PATH?token=PRIVATE_ALIAS_QUERY"
	const password = "PRIVATE_NODE_PASSWORD"
	const oldNodeID = "deadbeef1234abcd"
	const publicProfileID = "aa11bb22cc33dd44"
	node := model.Node{Protocol: model.Trojan, Name: "label " + password + " https://node.test/PRIVATE_NODE_PATH", Server: "edge.test", Port: 443, Password: password, TLS: &model.TLS{Enabled: true, ServerName: "sni.test"}}
	p, err := profile.NewProfile(sourceURL, profile.SourceSubscription, sourceURL, []model.Node{node})
	if err != nil {
		t.Fatal(err)
	}
	p.ID = publicProfileID
	p.Servers[0].ID = oldNodeID
	if err := d.store.Add(p); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	d.state.Profile = publicProfileID
	d.state.Node = oldNodeID
	d.mu.Unlock()
	d.emitLog(LogWarn, "raw URL "+sourceURL+" raw old "+publicProfileID+" node "+oldNodeID)
	runner.mu.Lock()
	runner.logs = []string{"raw https://engine.test/PRIVATE_ENGINE_PATH?key=PRIVATE_ENGINE_QUERY old " + oldNodeID}
	runner.mu.Unlock()
	response := d.Handle(context.Background(), Request{Cmd: CmdListProfiles})
	if !response.Ok {
		t.Fatal(response.Error)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	logs, _ := json.Marshal(d.logs.snapshot())
	bundle := d.CollectDiagnostics().Text
	combined := string(encoded) + string(logs) + bundle + d.nodeLabel(publicProfileID, oldNodeID)
	for _, canary := range []string{"PRIVATE_ALIAS_PATH", "PRIVATE_ALIAS_QUERY", password, "PRIVATE_NODE_PATH", oldNodeID, "PRIVATE_ENGINE_PATH", "PRIVATE_ENGINE_QUERY"} {
		if strings.Contains(combined, canary) {
			t.Errorf("redaction leaked synthetic canary %q", canary)
		}
	}
	if !strings.Contains(string(encoded), publicProfileID) || !strings.Contains(bundle, publicProfileID) {
		t.Fatal("typed public Profile.ID was treated as a credential-derived node hash")
	}
	if strings.Contains(bundle, "raw old "+publicProfileID) || strings.Contains(bundle, d.store.Dir()) {
		t.Fatal("free text or private repository path escaped diagnostics policy")
	}
	stored, _ := d.store.Get(publicProfileID)
	if stored.URL != sourceURL || stored.Servers[0].Password != password || stored.Servers[0].Name != node.Name {
		t.Fatal("boundary redaction modified stored private source")
	}
}
func TestGenericURLAliasImportReplyAndLogAreRedacted(t *testing.T) {
	d, _ := newTestDaemon(t)
	const sourceURL = "https://import.test/PRIVATE_IMPORT_PATH?token=PRIVATE_IMPORT_QUERY"
	d.fetch = func(context.Context, string) ([]byte, http.Header, error) {
		return []byte("trojan://PRIVATE_IMPORT_PASSWORD@edge.test:443?security=tls&sni=cover.test#PRIVATE_IMPORT_PASSWORD"), http.Header{}, nil
	}
	response := d.Handle(context.Background(), Request{Cmd: CmdImportSubscription, URL: sourceURL, Name: sourceURL})
	if !response.Ok {
		t.Fatal(response.Error)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	for _, canary := range []string{"PRIVATE_IMPORT_PATH", "PRIVATE_IMPORT_QUERY", "PRIVATE_IMPORT_PASSWORD"} {
		if strings.Contains(string(encoded), canary) {
			t.Error("import IPC leaked", canary)
		}
	}
	if stored := d.store.List(); len(stored) != 1 || stored[0].URL != sourceURL || stored[0].Servers[0].Password != "PRIVATE_IMPORT_PASSWORD" {
		t.Fatal("redaction changed imported private payload")
	}
}
