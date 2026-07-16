package control

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Divaaaan/tenebra/core/buildinfo"
	"github.com/Divaaaan/tenebra/core/model"
	"github.com/Divaaaan/tenebra/core/profile"
)

// secretSentinels are the credential values seeded into the test profile. Every
// one of them is a secret that a local user must not be able to read back over
// the control socket, so the redaction tests assert none appears in the
// marshalled list_profiles/status response. The subscription token is folded in
// via subURLWithToken below.
var secretSentinels = []string{
	"SECRET-UUID",
	"SECRET-PASSWORD",
	"SECRET-METHOD",
	"SECRET-PRIVATE-KEY",
	"SECRET-PEER-PUBLIC-KEY",
	"SECRET-PRE-SHARED-KEY",
	"SECRET-REALITY-PUBKEY",
	"SECRET-REALITY-SHORTID",
	"SECRET-SUB-TOKEN",
}

// subURLWithToken embeds the account token in the subscription URL's path, the
// way a real panel does. Redaction must drop it whole.
const subURLWithToken = "https://panel.example.invalid/sub/SECRET-SUB-TOKEN"

// secretNode builds a node deliberately carrying every credential kind the
// builder understands — the VLESS/VMess UUID, the password, the Shadowsocks
// method, the (Amnezia)WG key material, and the REALITY keys — alongside the
// non-secret display fields the UI renders. The display fields use distinctive
// values so a test can assert they survive redaction.
func secretNode() model.Node {
	return model.Node{
		Protocol: model.VLESS,
		Name:     "Display Node Name",
		Server:   "node.display.example",
		Port:     4443,
		UUID:     "SECRET-UUID",
		Password: "SECRET-PASSWORD",
		Method:   "SECRET-METHOD",
		TLS: &model.TLS{
			Enabled: true,
			Reality: &model.Reality{
				PublicKey: "SECRET-REALITY-PUBKEY",
				ShortID:   "SECRET-REALITY-SHORTID",
			},
		},
		WireGuard: &model.WireGuard{
			PrivateKey:    "SECRET-PRIVATE-KEY",
			PeerPublicKey: "SECRET-PEER-PUBLIC-KEY",
			PreSharedKey:  "SECRET-PRE-SHARED-KEY",
		},
	}
}

// daemonWithSecretProfile builds a daemon over a temp store seeded with one
// subscription profile whose URL carries a token and whose single node carries
// every secret (see secretNode). It returns the daemon and the stored profile so
// a test can reference its IDs.
func daemonWithSecretProfile(t *testing.T) (*Daemon, profile.Profile) {
	t.Helper()
	store, err := profile.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	p, err := profile.NewProfile("Provider", profile.SourceSubscription, subURLWithToken, []model.Node{secretNode()})
	if err != nil {
		t.Fatalf("new profile: %v", err)
	}
	if err := store.Add(p); err != nil {
		t.Fatalf("add profile: %v", err)
	}
	return NewDaemon(store, newFakeRunner()), p
}

// assertNoSecrets fails if any seeded secret sentinel appears anywhere in the
// marshalled bytes. It is the core invariant the redaction guarantees: the wire
// response is readable by any local user, so it must be secret-free.
func assertNoSecrets(t *testing.T, raw []byte) {
	t.Helper()
	s := string(raw)
	for _, secret := range secretSentinels {
		if strings.Contains(s, secret) {
			t.Errorf("wire response leaks secret %q:\n%s", secret, s)
		}
	}
}

// TestListProfilesRedactsSecrets checks that list_profiles serialises only the
// display fields: none of the node credentials nor the subscription token reach
// the wire, while the fields the UI renders (id, name, host, port, protocol)
// still do.
func TestListProfilesRedactsSecrets(t *testing.T) {
	d, p := daemonWithSecretProfile(t)

	resp := d.handleListProfiles(Request{ID: 1, Cmd: CmdListProfiles})
	if !resp.Ok {
		t.Fatalf("list_profiles failed: %s", resp.Error)
	}
	assertNoSecrets(t, resp.Data)

	// The display fields the UI needs must survive redaction.
	for _, want := range []string{p.ID, "Display Node Name", "node.display.example", "4443", string(model.VLESS)} {
		if !strings.Contains(string(resp.Data), want) {
			t.Errorf("list_profiles dropped display field %q:\n%s", want, resp.Data)
		}
	}

	// The URL must be gone entirely — not merely token-masked — so decode the
	// response and confirm no url field survived on the profile.
	var out struct {
		Profiles []map[string]json.RawMessage `json:"profiles"`
	}
	if err := json.Unmarshal(resp.Data, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(out.Profiles) != 1 {
		t.Fatalf("got %d profiles, want 1", len(out.Profiles))
	}
	if _, ok := out.Profiles[0]["url"]; ok {
		t.Errorf("redacted profile still carries a url field: %s", resp.Data)
	}
}

// TestStatusReportsDaemonVersion locks the version handshake: every status
// (and thus every State snapshot) names the build the daemon came from, so a
// GUI updated ahead of a hand-installed daemon (the macOS case — the in-app
// updater refreshes only the .app, never the root LaunchDaemon) can surface
// the skew instead of letting the toggles of newer commands die silently.
func TestStatusReportsDaemonVersion(t *testing.T) {
	if buildinfo.Version == "" {
		t.Fatal("buildinfo.Version is empty")
	}

	d, _ := daemonWithSecretProfile(t)

	resp := d.handleStatus(Request{ID: 1, Cmd: CmdStatus})
	if !resp.Ok {
		t.Fatalf("status failed: %s", resp.Error)
	}
	var st State
	if err := json.Unmarshal(resp.Data, &st); err != nil {
		t.Fatalf("unmarshal state: %v", err)
	}
	if st.DaemonVersion != buildinfo.Version {
		t.Errorf("status daemon_version = %q, want %q", st.DaemonVersion, buildinfo.Version)
	}
}

// TestStatusCarriesNoSecrets locks the invariant for the status response too:
// even with the connection state pointed at the secret-bearing profile and node,
// the status reply (which carries only scalar identifiers) leaks nothing.
func TestStatusCarriesNoSecrets(t *testing.T) {
	d, p := daemonWithSecretProfile(t)

	// Drive the reported state to "connected" on the secret profile/node so the
	// status snapshot names them by ID — the IDs are safe, the secrets are not.
	d.mu.Lock()
	d.state.State = StateConnected
	d.state.Profile = p.ID
	d.state.Node = p.Servers[0].ID
	d.mu.Unlock()

	resp := d.handleStatus(Request{ID: 1, Cmd: CmdStatus})
	if !resp.Ok {
		t.Fatalf("status failed: %s", resp.Error)
	}
	assertNoSecrets(t, resp.Data)
}
