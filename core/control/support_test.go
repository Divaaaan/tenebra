package control

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Divaaaan/tenebra/core/buildinfo"
	"github.com/Divaaaan/tenebra/core/model"
	"github.com/Divaaaan/tenebra/core/profile"
	"github.com/Divaaaan/tenebra/core/tunguard"
)

// TestScrubSecretsMasksWhatTheDesktopMasks holds the Go scrubber to the same
// rules as ui-desktop/src/lib/diagnostics.ts. The two produce bundles a user may
// paste into the same conversation, so a shape masked in one and left bare in
// the other would be a leak with a false sense of safety over it.
func TestScrubSecretsMasksWhatTheDesktopMasks(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "managed subscription token",
			in:   "refresh https://vpsxd.pro/sub/9f8a7b6c5d4e failed",
			want: "refresh https://vpsxd.pro/sub/*** failed",
		},
		{
			name: "second managed host",
			in:   "GET https://vpnxd.pro/api/v1/sub/TOKEN-VALUE",
			want: "GET https://vpnxd.pro/api/v1/sub/***",
		},
		{
			name: "share link userinfo",
			in:   "imported vless://d0a1b2c3@node.example:443?sni=x",
			want: "imported vless://***@node.example:443?sni=x",
		},
		{
			name: "trojan password",
			in:   "trojan://hunter2@edge.example:8443",
			want: "trojan://***@edge.example:8443",
		},
		{
			name: "bare uuid",
			in:   "outbound uses 11111111-2222-3333-4444-555555555555 for auth",
			want: "outbound uses *** for auth",
		},
		{
			name: "hosts, ports and errors survive",
			in:   "connect: sing-box would not start for vless ex-01: dial tcp 203.0.113.7:443: timeout",
			want: "connect: sing-box would not start for vless ex-01: dial tcp 203.0.113.7:443: timeout",
		},
	}
	for _, c := range cases {
		if got := scrubSecrets(c.in); got != c.want {
			t.Errorf("%s:\n scrubSecrets(%q)\n            = %q\n       want = %q", c.name, c.in, got, c.want)
		}
	}
}

// bundleDaemon builds a daemon holding one profile whose subscription URL and
// node credentials are the sentinels the redaction tests look for.
func bundleDaemon(t *testing.T) (*Daemon, *fakeRunner) {
	t.Helper()
	d, runner := newTestDaemon(t)
	p, err := profile.NewProfile("Test Profile", "subscription", subURLWithToken,
		[]model.Node{secretNode()})
	if err != nil {
		t.Fatalf("build profile: %v", err)
	}
	if err := d.store.Add(p); err != nil {
		t.Fatalf("add profile: %v", err)
	}
	return d, runner
}

// TestDiagnosticsMasksSecretsThatReachedTheLog is the guarantee the whole
// feature rests on: the bundle is written to be pasted into a chat with a
// stranger, so a token that happened to land in a log line must not ride along.
func TestDiagnosticsMasksSecretsThatReachedTheLog(t *testing.T) {
	d, runner := bundleDaemon(t)
	// Every channel the bundle draws text from, each carrying a different secret
	// shape: the daemon's own log, and sing-box's captured output.
	d.emitLog(LogWarn, "refresh failed for https://vpsxd.pro/sub/SECRET-SUB-TOKEN")
	d.emitLog(LogInfo, "imported vless://11111111-2222-3333-4444-555555555555@edge.example:443")
	runner.mu.Lock()
	runner.logs = []string{"outbound id 11111111-2222-3333-4444-555555555555 selected"}
	runner.mu.Unlock()

	text := d.CollectDiagnostics().Text

	for _, leaked := range []string{
		"SECRET-SUB-TOKEN",
		"11111111-2222-3333-4444-555555555555",
	} {
		if strings.Contains(text, leaked) {
			t.Errorf("the bundle carries %q:\n%s", leaked, text)
		}
	}
	// The context around the secret is the point of a diagnostics dump and has to
	// survive: masking the host as well would leave a line nobody can act on.
	for _, kept := range []string{"vpsxd.pro", "edge.example", "refresh failed"} {
		if !strings.Contains(text, kept) {
			t.Errorf("the bundle dropped useful context %q:\n%s", kept, text)
		}
	}
}

// TestDiagnosticsNeverCarriesAStoredSubscriptionURL: the profile section lists
// what is stored, and a managed subscription's URL is the token. It must not be
// printed at all — masked or otherwise.
func TestDiagnosticsNeverCarriesAStoredSubscriptionURL(t *testing.T) {
	d, _ := bundleDaemon(t)
	text := d.CollectDiagnostics().Text
	for _, secret := range secretSentinels {
		if strings.Contains(text, secret) {
			t.Errorf("the bundle carries the stored credential %q:\n%s", secret, text)
		}
	}
	if strings.Contains(text, subURLWithToken) {
		t.Errorf("the bundle carries the stored subscription URL:\n%s", text)
	}
	// The profile itself still has to be identifiable, or the bundle cannot say
	// which subscription the failure was against.
	if !strings.Contains(text, "Test Profile") {
		t.Errorf("the bundle does not name the stored profile:\n%s", text)
	}
}

// TestDiagnosticsCarriesTheStateAndBuild covers the header: a report that does
// not say which build produced it costs a round trip before anyone can even
// start reading it.
func TestDiagnosticsCarriesTheStateAndBuild(t *testing.T) {
	d, _ := bundleDaemon(t)
	b := d.CollectDiagnostics()

	for _, want := range []string{
		"Tenebra core diagnostics",
		"Core version:  " + buildinfo.Version,
		"Log level:     " + LogInfo,
		"State:         idle",
		"Bypass bundle: unknown",
	} {
		if !strings.Contains(b.Text, want) {
			t.Errorf("the bundle is missing %q:\n%s", want, b.Text)
		}
	}
	if !strings.HasPrefix(b.Filename, "tenebra-diagnostics-") || !strings.HasSuffix(b.Filename, ".txt") {
		t.Errorf("suggested filename = %q, want a timestamped .txt", b.Filename)
	}
	if strings.ContainsAny(b.Filename, `/\`) {
		t.Errorf("suggested filename %q carries a path", b.Filename)
	}
}

// TestDiagnosticsReadsTheLogFileTail: on a service where no UI was ever
// attached, the file is the record. The bundle has to include it, not just the
// in-memory ring the running process happens to hold.
func TestDiagnosticsReadsTheLogFileTail(t *testing.T) {
	d, _ := bundleDaemon(t)
	d.SetLogTail(func(n int) []string {
		return []string{"2026-08-24 01:00:00 info: from the service log file"}
	})
	text := d.CollectDiagnostics().Text
	if !strings.Contains(text, "from the service log file") {
		t.Errorf("the bundle skipped the log file tail:\n%s", text)
	}
	if !strings.Contains(text, "Log file (last 1 lines)") {
		t.Errorf("the bundle has no log file section:\n%s", text)
	}
}

// TestDiagnosticsWithoutALogFileStillReports: the sidecar and console modes have
// no file, and the bundle must degrade to the ring rather than to nothing.
func TestDiagnosticsWithoutALogFileStillReports(t *testing.T) {
	d, _ := bundleDaemon(t)
	d.emitLog(LogInfo, "only in the ring")
	text := d.CollectDiagnostics().Text
	if strings.Contains(text, "Log file") {
		t.Errorf("a daemon with no log file still printed a log file section:\n%s", text)
	}
	if !strings.Contains(text, "only in the ring") {
		t.Errorf("the bundle skipped the in-memory ring:\n%s", text)
	}
}

// TestDiagnosticsRendersTheRouteTable: the tun-conflict guard reasons entirely
// from this enumeration, so a bundle that shows it lets a maintainer re-derive
// the guard's verdict instead of asking the user what else was running.
func TestDiagnosticsRendersTheRouteTable(t *testing.T) {
	d, _ := bundleDaemon(t)
	d.SetInterfaceProbe(func() ([]tunguard.Iface, error) {
		return []tunguard.Iface{
			{Name: "Ethernet", HasDefaultRoute: true, RouteMetric: 25},
			{Name: "Hiddify Tunnel", HasDefaultRoute: true, RouteMetric: 5},
			{Name: "Loopback", HasDefaultRoute: false},
		}, nil
	})
	text := d.CollectDiagnostics().Text

	if !strings.Contains(text, "Interfaces and default routes") {
		t.Fatalf("the bundle has no interface section:\n%s", text)
	}
	// The foreign tunnel must be classified as one even though the adapter could
	// not say so itself — that name heuristic is what the guard actually uses.
	line := ""
	for _, ln := range strings.Split(text, "\n") {
		if strings.HasPrefix(ln, "Hiddify Tunnel") {
			line = ln
		}
	}
	if line == "" {
		t.Fatalf("the interface table does not list the foreign tunnel:\n%s", text)
	}
	if !strings.Contains(line, "yes") || !strings.Contains(line, "5") {
		t.Errorf("interface line %q does not report it as a tunnel holding a default route at metric 5", line)
	}
}

// TestDiagnosticsWithNoRouteEnumerationSaysSo: nil is the honest answer on a
// platform with no adapter, and reading it as "no other tunnel is running" is
// exactly the mistake the guard's own comment warns about.
func TestDiagnosticsWithNoRouteEnumerationSaysSo(t *testing.T) {
	d, _ := bundleDaemon(t)
	text := d.CollectDiagnostics().Text
	if !strings.Contains(text, "no enumeration on this platform") {
		t.Errorf("the bundle does not distinguish 'nothing found' from 'could not look':\n%s", text)
	}
}

// TestDiagnosticsReportsTheConnectWalk: the walk is the record of which exits
// were tried and why the chosen one won, and it is dropped on teardown — so a
// bundle taken after a failure has to carry the last one.
func TestDiagnosticsReportsTheConnectWalk(t *testing.T) {
	d, _ := bundleDaemon(t)
	d.mu.Lock()
	d.attempts = &attemptsEvent{
		Outcome: AttemptOutcomeExhausted,
		Items: []attemptItem{
			{Seq: 1, Protocol: "vless", Node: "ex-01", Status: AttemptBlocked, LastGood: true, Reason: "censored"},
			{Seq: 2, Protocol: "hysteria2", Node: "ex-02", Status: AttemptBlocked, Strategy: "fragment"},
		},
	}
	d.mu.Unlock()

	text := d.CollectDiagnostics().Text
	for _, want := range []string{"outcome: exhausted", "ex-01", "last-good", "reason=censored", "strategy=fragment"} {
		if !strings.Contains(text, want) {
			t.Errorf("the walk section is missing %q:\n%s", want, text)
		}
	}
}

// TestCollectDiagnosticsCommandAnswers wires the whole thing through the
// protocol so the desktop path is covered, not just the assembler.
func TestCollectDiagnosticsCommandAnswers(t *testing.T) {
	d, _ := bundleDaemon(t)
	resp := d.Handle(context.Background(), Request{ID: 7, Cmd: CmdCollectDiagnostics})
	if !resp.Ok {
		t.Fatalf("collect_diagnostics failed: %s", resp.Error)
	}
	if resp.ID != 7 {
		t.Errorf("response id = %d, want 7", resp.ID)
	}
	var got SupportBundle
	if err := json.Unmarshal(resp.Data, &got); err != nil {
		t.Fatalf("decode response data: %v", err)
	}
	if !strings.Contains(got.Text, "Tenebra core diagnostics") {
		t.Errorf("the command's payload is not the bundle: %q", got.Text)
	}
	if got.Filename == "" {
		t.Error("the command's payload carries no suggested filename")
	}
}

func TestTruncateKeepsColumnsIntact(t *testing.T) {
	if got := truncate("short", 10); got != "short" {
		t.Errorf("truncate kept = %q, want the input unchanged", got)
	}
	got := truncate("an extremely long profile name", 10)
	if len([]rune(got)) != 10 {
		t.Errorf("truncate(%q, 10) = %q (%d runes), want 10", "an extremely long profile name", got, len([]rune(got)))
	}
}
