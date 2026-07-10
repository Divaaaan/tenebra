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

// The URL token and the two node secrets carried by vlessLink (see
// refresh_test.go). The end-to-end import/refresh commands parse that link, so a
// redacted reply must not echo either the token in the source URL or the node's
// UUID / REALITY public key.
const (
	e2eURLToken  = "E2E-SUB-TOKEN"
	e2eNodeUUID  = "11111111-2222-3333-4444-555555555555"
	e2eRealityPK = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
)

// assertRedactedProfileWire fails if a single marshalled {profile: ...} response
// leaks any node credential or the token-bearing URL, and confirms the display
// fields the UI needs still survive. It decodes the response so it can assert the
// url field is gone whole rather than merely token-masked, matching the
// list_profiles invariant.
func assertRedactedProfileWire(t *testing.T, raw []byte, p profile.Profile) {
	t.Helper()
	assertNoSecrets(t, raw)

	for _, want := range []string{p.ID, "Display Node Name", "node.display.example", "4443", string(model.VLESS)} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("response dropped display field %q:\n%s", want, raw)
		}
	}

	var out struct {
		Profile map[string]json.RawMessage `json:"profile"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if _, ok := out.Profile["url"]; ok {
		t.Errorf("redacted profile still carries a url field: %s", raw)
	}
}

// TestRedactProfileKeepsInsecureFlag guards a cross-feature regression: the
// redacted view must still carry the per-node insecure (skip-cert-verify) flag,
// because the desktop "TLS verification off" badge reads it from list_profiles.
// Redaction strips secrets, but insecure is a warning, not a secret — dropping
// it would silently hide the badge.
func TestRedactProfileKeepsInsecureFlag(t *testing.T) {
	p := profile.Profile{
		ID:   "p1",
		Name: "p",
		Servers: []profile.Server{
			{ID: "n-secure", Node: model.Node{Protocol: model.VLESS, Server: "a.example", Port: 443}},
			{ID: "n-insecure", Node: model.Node{Protocol: model.Trojan, Server: "b.example", Port: 443,
				TLS: &model.TLS{Enabled: true, Insecure: true}}},
		},
	}
	raw, err := json.Marshal(redactProfile(p))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out struct {
		Nodes []struct {
			ID       string `json:"id"`
			Insecure bool   `json:"insecure"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	byID := map[string]bool{}
	for _, n := range out.Nodes {
		byID[n.ID] = n.Insecure
	}
	if byID["n-insecure"] != true {
		t.Errorf("redacted insecure node lost its insecure flag: %s", raw)
	}
	if byID["n-secure"] != false {
		t.Errorf("secure node should not report insecure: %s", raw)
	}
}

// TestProfileResultRedactsSecrets locks the wire shape shared by
// import_subscription, import_link, and refresh_subscription: profileResult must
// serialise the redacted view, so none of the node credentials nor the
// subscription token reach the socket.
func TestProfileResultRedactsSecrets(t *testing.T) {
	d, p := daemonWithSecretProfile(t)
	resp := d.profileResult(1, p)
	if !resp.Ok {
		t.Fatalf("profileResult failed: %s", resp.Error)
	}
	assertRedactedProfileWire(t, resp.Data, p)
}

// TestImportLinksResultRedactsSecrets locks the batch-import wire shape: the
// {profile, imported, skipped} reply must carry the redacted profile and still
// report the counts.
func TestImportLinksResultRedactsSecrets(t *testing.T) {
	d, p := daemonWithSecretProfile(t)
	resp := d.importLinksResult(1, p, 3, 2)
	if !resp.Ok {
		t.Fatalf("importLinksResult failed: %s", resp.Error)
	}
	assertRedactedProfileWire(t, resp.Data, p)

	var out struct {
		Imported int `json:"imported"`
		Skipped  int `json:"skipped"`
	}
	if err := json.Unmarshal(resp.Data, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if out.Imported != 3 || out.Skipped != 2 {
		t.Errorf("counts = imported %d, skipped %d; want 3, 2", out.Imported, out.Skipped)
	}
}

// TestImportSubscriptionCommandRedactsSecrets drives the real import_subscription
// command against a fetched body and asserts the reply leaks neither the URL
// token nor the parsed node's credentials — while the stored profile still holds
// them for the connect path.
func TestImportSubscriptionCommandRedactsSecrets(t *testing.T) {
	subURL := "https://panel.example.invalid/sub/" + e2eURLToken
	f := &fakeFetch{bodies: map[string]string{subURL: vlessLink}}
	d, store := daemonWithFetch(t, f)

	resp := d.handleImportSubscription(context.Background(), Request{ID: 1, Cmd: CmdImportSubscription, URL: subURL, Name: "Provider"})
	if !resp.Ok {
		t.Fatalf("import_subscription failed: %s", resp.Error)
	}
	assertNoE2ESecrets(t, resp.Data)

	// The store keeps the full node (connect path) and the token-bearing URL.
	assertStoreKeepsSecrets(t, store)
}

// TestRefreshSubscriptionCommandRedactsSecrets drives refresh_subscription and
// asserts both the reply and every event it emits (the profiles hint) stay
// secret-free, while the store retains the full node and token URL.
func TestRefreshSubscriptionCommandRedactsSecrets(t *testing.T) {
	subURL := "https://panel.example.invalid/sub/" + e2eURLToken
	f := &fakeFetch{
		bodies:  map[string]string{subURL: vlessLink},
		headers: map[string]http.Header{subURL: http.Header{}},
	}
	d, store := daemonWithFetch(t, f)
	p := seedSubscription(t, store, "Provider", subURL)

	rec := &eventRecorder{}
	d.SetEmitter(rec.emit)

	resp := d.handleRefreshSubscription(context.Background(), Request{ID: 1, Cmd: CmdRefreshSub, Profile: p.ID})
	if !resp.Ok {
		t.Fatalf("refresh_subscription failed: %s", resp.Error)
	}
	assertNoE2ESecrets(t, resp.Data)

	// The profiles event (and any other emitted event) must not carry secrets.
	for _, ev := range rec.events {
		body, err := json.Marshal(ev.body)
		if err != nil {
			t.Fatalf("marshal event %q: %v", ev.name, err)
		}
		assertNoE2ESecrets(t, body)
	}
	assertStoreKeepsSecrets(t, store)
}

// TestImportLinkCommandRedactsSecrets drives import_link and asserts the reply
// carries no node credentials.
func TestImportLinkCommandRedactsSecrets(t *testing.T) {
	d, _ := daemonWithFetch(t, &fakeFetch{})
	resp := d.handleImportLink(Request{ID: 1, Cmd: CmdImportLink, Link: vlessLink})
	if !resp.Ok {
		t.Fatalf("import_link failed: %s", resp.Error)
	}
	assertNoE2ESecrets(t, resp.Data)
}

// TestImportLinksCommandRedactsSecrets drives import_links and asserts the reply
// carries no node credentials.
func TestImportLinksCommandRedactsSecrets(t *testing.T) {
	d, _ := daemonWithFetch(t, &fakeFetch{})
	resp := d.handleImportLinks(Request{ID: 1, Cmd: CmdImportLinks, Links: []string{vlessLink}})
	if !resp.Ok {
		t.Fatalf("import_links failed: %s", resp.Error)
	}
	assertNoE2ESecrets(t, resp.Data)
}

// assertNoE2ESecrets fails if the URL token or either node secret from the parsed
// vlessLink appears in the marshalled bytes.
func assertNoE2ESecrets(t *testing.T, raw []byte) {
	t.Helper()
	s := string(raw)
	for _, secret := range []string{e2eURLToken, e2eNodeUUID, e2eRealityPK} {
		if strings.Contains(s, secret) {
			t.Errorf("wire response leaks secret %q:\n%s", secret, s)
		}
	}
}

// assertStoreKeepsSecrets confirms the on-disk store still holds the full node
// credential and token-bearing URL — redaction is a wire-only projection and must
// never strip the connect path's data.
func assertStoreKeepsSecrets(t *testing.T, store *profile.Store) {
	t.Helper()
	var found bool
	for _, p := range store.List() {
		if strings.Contains(p.URL, e2eURLToken) {
			found = true
		}
		for _, s := range p.Servers {
			if s.UUID == e2eNodeUUID {
				found = true
			}
		}
	}
	if !found {
		t.Error("store no longer holds the full node/URL secrets; redaction must not touch the store")
	}
}

// eventRecorder captures the daemon's emitted events so a test can assert their
// bodies stay secret-free.
type eventRecorder struct {
	events []recordedEvent
}

type recordedEvent struct {
	name string
	body any
}

func (r *eventRecorder) emit(name string, body any) {
	r.events = append(r.events, recordedEvent{name: name, body: body})
}
